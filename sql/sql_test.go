package sql_test

import (
	"bytes"
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/health"
	"github.com/dygoframework/dygo/sql"
)

// fakeDriver is a database/sql driver whose ping result the test controls.
type fakeDriver struct{}

type fakeState struct {
	pingErr error
	opened  atomic.Int32
	closed  atomic.Int32
}

var (
	statesMu sync.Mutex
	states   = map[string]*fakeState{}
)

func newState(t *testing.T) (*fakeState, string) {
	t.Helper()
	st := &fakeState{}
	key := strings.ReplaceAll(t.Name(), "/", "_")
	statesMu.Lock()
	states[key] = st
	statesMu.Unlock()
	return st, "fake://user:s3cret@db.local/" + key
}

func init() { stdsql.Register("dygofake", fakeDriver{}) }

func (fakeDriver) Open(dsn string) (driver.Conn, error) {
	statesMu.Lock()
	st := states[dsn[strings.LastIndex(dsn, "/")+1:]]
	statesMu.Unlock()
	if st == nil {
		return nil, fmt.Errorf("no state for %s", dsn)
	}
	st.opened.Add(1)
	return &fakeConn{st: st}, nil
}

type fakeConn struct{ st *fakeState }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *fakeConn) Close() error                        { c.st.closed.Add(1); return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (c *fakeConn) Ping(context.Context) error          { return c.st.pingErr }

// fakeDialect passes the URL through and classifies codeErr values.
type fakeDialect struct{}

type codeErr struct{ code string }

func (e *codeErr) Error() string { return "fake error " + e.code }

func (fakeDialect) Name() string           { return "fake" }
func (fakeDialect) DriverName() string     { return "dygofake" }
func (fakeDialect) Placeholder(int) string { return "?" }
func (fakeDialect) BackslashEscapes() bool { return true }
func (fakeDialect) Quote(s string) string  { return "`" + s + "`" }
func (fakeDialect) Returning() bool        { return false }
func (fakeDialect) DSN(u string) (string, error) {
	if !strings.HasPrefix(u, "fake://") {
		return "", errors.New("not a fake URL")
	}
	return u, nil
}

func (fakeDialect) Classify(err error) (sql.Kind, string) {
	var ce *codeErr
	if !errors.As(err, &ce) {
		return sql.KindOther, ""
	}
	switch ce.code {
	case "dup":
		return sql.KindDuplicate, "users_email_key"
	case "fk":
		return sql.KindForeignKey, ""
	case "null":
		return sql.KindConstraint, "email"
	case "retry":
		return sql.KindRetryable, ""
	}
	return sql.KindOther, ""
}

func TestOpenAppliesPoolSettings(t *testing.T) {
	_, url := newState(t)
	db, err := sql.Open(context.Background(), fakeDialect{}, sql.Config{URL: url, MaxOpen: 7, MaxIdle: 3, ConnectTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("MaxOpenConnections = %d, want 7", got)
	}
	if db.Name() != "fake" || db.Dialect().Name() != "fake" {
		t.Fatalf("name %q", db.Name())
	}
}

func TestOpenFailsWithoutLeakingPassword(t *testing.T) {
	st, url := newState(t)
	st.pingErr = errors.New("auth failed for " + url)
	_, err := sql.Open(context.Background(), fakeDialect{}, sql.Config{URL: url})
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("password leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "xxxxx") || !errors.Is(err, st.pingErr) {
		t.Fatalf("err = %v", err)
	}
	if st.closed.Load() != st.opened.Load() {
		t.Fatalf("opened %d, closed %d", st.opened.Load(), st.closed.Load())
	}
}

func TestOpenBadURL(t *testing.T) {
	_, err := sql.Open(context.Background(), fakeDialect{}, sql.Config{URL: "mysql://u:pw123@h/db"})
	if err == nil || strings.Contains(err.Error(), "pw123") || !strings.Contains(err.Error(), "bad URL") {
		t.Fatalf("err = %v", err)
	}
	if _, err := sql.Open(context.Background(), fakeDialect{}, sql.Config{}); err == nil {
		t.Fatal("empty URL: want error")
	}
}

func TestOpenRespectsConnectTimeout(t *testing.T) {
	st, url := newState(t)
	st.pingErr = context.DeadlineExceeded
	_, err := sql.Open(context.Background(), fakeDialect{}, sql.Config{URL: url, ConnectTimeout: time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

type repo struct{ db *sql.DB }

func TestModuleProvidesAndClosesPool(t *testing.T) {
	st, url := newState(t)
	root := &dygo.Module{
		Name:      "app",
		Imports:   []*dygo.Module{sql.Module(fakeDialect{}, sql.WithConfig(sql.Config{URL: url}))},
		Providers: []dygo.Provider{dygo.Provide(func(db *sql.DB) *repo { return &repo{db} })},
	}
	app, err := dygo.CreateApplicationContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	r := dygo.MustGet[*repo](app)
	if r.db == nil || r.db.Name() != "db" {
		t.Fatalf("db = %+v", r.db)
	}
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.db.PingContext(context.Background()); err == nil {
		t.Fatal("pool still open after shutdown")
	}
	if st.closed.Load() != st.opened.Load() {
		t.Fatalf("opened %d, closed %d", st.opened.Load(), st.closed.Load())
	}
}

func TestModuleReadsEnvWithPrefix(t *testing.T) {
	_, url := newState(t)
	t.Setenv("REPORTS_DATABASE_URL", url)
	t.Setenv("REPORTS_DB_MAX_OPEN", "4")
	type params struct {
		dygo.In
		Reports *sql.DB `name:"reports"`
	}
	var got *sql.DB
	root := &dygo.Module{
		Name:    "app",
		Imports: []*dygo.Module{sql.Module(fakeDialect{}, sql.Prefix("REPORTS_"), sql.Named("reports"), sql.Files())},
		Providers: []dygo.Provider{dygo.Provide(func(p params) *repo {
			got = p.Reports
			return &repo{p.Reports}
		})},
	}
	app, err := dygo.CreateApplicationContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	if got.Name() != "reports" || got.Stats().MaxOpenConnections != 4 {
		t.Fatalf("name %q max %d", got.Name(), got.Stats().MaxOpenConnections)
	}
	if db, err := dygo.GetNamed(app, sql.Token("reports")); err != nil || db != got {
		t.Fatalf("GetNamed: %v", err)
	}
}

func TestModuleLogsTheDatabase(t *testing.T) {
	_, url := newState(t)
	t.Setenv("DATABASE_URL", url)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	root := &dygo.Module{
		Name: "app",
		Imports: []*dygo.Module{
			{Name: "logger", Global: true, Providers: []dygo.Provider{dygo.Value(log)}, Exports: []dygo.Token{dygo.TypeOf[*slog.Logger]()}},
			sql.Module(fakeDialect{}, sql.Files()),
		},
	}
	app, err := dygo.CreateApplicationContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(context.Background())
	out := buf.String()
	if !strings.Contains(out, "database connected") || !strings.Contains(out, "db.local/") || strings.Contains(out, "s3cret") {
		t.Fatalf("log:\n%s", out)
	}
}

func TestModuleBootFailsOnMissingURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	root := &dygo.Module{Name: "app", Imports: []*dygo.Module{sql.Module(fakeDialect{}, sql.Files())}}
	_, err := dygo.CreateApplicationContext(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("err = %v", err)
	}
}

func TestHealthIndicator(t *testing.T) {
	st, url := newState(t)
	root := &dygo.Module{Name: "app", Imports: []*dygo.Module{
		sql.Module(fakeDialect{}, sql.WithConfig(sql.Config{URL: url})),
		health.Module(health.Options{}),
	}}
	srv := dygotest.New(root).CompileHTTP(t)
	srv.GET("/health/ready").Do(t).Status(t, http.StatusOK)
	st.pingErr = errors.New("down")
	srv.GET("/health/ready").Do(t).Status(t, http.StatusServiceUnavailable)
}

func TestMapError(t *testing.T) {
	_, url := newState(t)
	db, err := sql.Open(context.Background(), fakeDialect{}, sql.Config{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cases := []struct {
		in     error
		kind   error
		status int
		code   string
	}{
		{stdsql.ErrNoRows, sql.ErrNotFound, 404, "NOT_FOUND"},
		{fmt.Errorf("insert: %w", &codeErr{"dup"}), sql.ErrDuplicate, 409, "DUPLICATE"},
		{&codeErr{"fk"}, sql.ErrForeignKey, 409, "FOREIGN_KEY"},
		{&codeErr{"null"}, sql.ErrConstraint, 422, "CONSTRAINT"},
		{&codeErr{"retry"}, sql.ErrRetryable, 500, ""},
	}
	for _, c := range cases {
		got := db.MapError(c.in)
		if !errors.Is(got, c.kind) {
			t.Errorf("%v: not %v", c.in, c.kind)
		}
		if !errors.Is(got, c.in) {
			t.Errorf("%v: original error lost", c.in)
		}
		var conv errs.Converter
		if !errors.As(got, &conv) {
			t.Fatalf("%v: not an errs.Converter", c.in)
		}
		he := conv.HTTPError()
		if he.Status != c.status || he.Code != c.code {
			t.Errorf("%v: status %d code %q", c.in, he.Status, he.Code)
		}
	}

	var se *sql.Error
	if errors.As(db.MapError(&codeErr{"dup"}), &se); se == nil || se.Constraint != "users_email_key" {
		t.Fatalf("constraint = %+v", se)
	}
	other := errors.New("boom")
	if db.MapError(other) != other || db.MapError(nil) != nil {
		t.Fatal("unknown errors must pass through")
	}
	once := db.MapError(&codeErr{"dup"})
	if db.MapError(once) != once {
		t.Fatal("mapping twice must not wrap again")
	}
}

func TestMapErrorInHTTPResponse(t *testing.T) {
	_, url := newState(t)
	ctrl := &dupController{}
	root := &dygo.Module{
		Name:        "app",
		Imports:     []*dygo.Module{sql.Module(fakeDialect{}, sql.WithConfig(sql.Config{URL: url}), sql.NoHealth())},
		Controllers: []dygo.Provider{dygo.Provide(func(db *sql.DB) *dupController { ctrl.db = db; return ctrl })},
	}
	srv := dygotest.New(root).CompileHTTP(t)
	res := srv.POST("/users").Do(t).Status(t, http.StatusConflict)
	if body := res.Text(); strings.Contains(body, "users_email_key") || !strings.Contains(body, "DUPLICATE") {
		t.Fatalf("body = %s", body)
	}
}

type dupController struct{ db *sql.DB }

func (c *dupController) Routes(r dygo.Router) {
	r.Post("/users", dygo.HandleNoInput(func(context.Context) (struct{}, error) {
		return struct{}{}, c.db.MapError(&codeErr{"dup"})
	}))
}
