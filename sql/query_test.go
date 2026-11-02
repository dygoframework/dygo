package sql_test

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/sql"
)

// recDriver records every statement and answers queries from a script.
type recDriver struct{}

type recorder struct {
	mu      sync.Mutex
	log     []string
	results map[string]recResult // by query text
	fails   map[string]int       // exec error for the first n runs of a query
	zero    map[string]bool      // exec reports no rows affected
	args    [][]driver.Value     // arguments of each exec and query
}

type recResult struct {
	cols []string
	rows [][]driver.Value
}

var rec = &recorder{}

func init() { stdsql.Register("dygorec", recDriver{}) }

func (r *recorder) add(s string) { r.mu.Lock(); r.log = append(r.log, s); r.mu.Unlock() }

func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.log
	r.log = nil
	return l
}

func (r *recorder) takeArgs() [][]driver.Value {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.args
	r.args = nil
	return a
}

func (r *recorder) addArgs(nv []driver.NamedValue) {
	vs := make([]driver.Value, len(nv))
	for i, v := range nv {
		vs[i] = v.Value
	}
	r.mu.Lock()
	r.args = append(r.args, vs)
	r.mu.Unlock()
}

// recExecResult reports 42 as the inserted id.
type recExecResult struct{ n int64 }

func (recExecResult) LastInsertId() (int64, error)   { return 42, nil }
func (r recExecResult) RowsAffected() (int64, error) { return r.n, nil }

func (recDriver) Open(string) (driver.Conn, error) { return &recConn{}, nil }

type recConn struct{}

func (*recConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("prepare not supported") }
func (*recConn) Close() error                        { return nil }
func (*recConn) Begin() (driver.Tx, error)           { return nil, errors.New("use BeginTx") }
func (*recConn) Ping(context.Context) error          { return nil }

func (*recConn) BeginTx(_ context.Context, o driver.TxOptions) (driver.Tx, error) {
	if o.ReadOnly {
		rec.add("BEGIN READ ONLY")
	} else {
		rec.add("BEGIN")
	}
	return recTx{}, nil
}

type recTx struct{}

func (recTx) Commit() error   { rec.add("COMMIT"); return nil }
func (recTx) Rollback() error { rec.add("ROLLBACK"); return nil }

func (*recConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	rec.add(q)
	rec.addArgs(args)
	rec.mu.Lock()
	n := rec.fails[q]
	zero := rec.zero[q]
	if n > 0 {
		rec.fails[q] = n - 1
	}
	rec.mu.Unlock()
	if n > 0 {
		code := "dup"
		if strings.Contains(q, "retry") {
			code = "retry"
		}
		return nil, &codeErr{code}
	}
	if zero {
		return recExecResult{0}, nil
	}
	return recExecResult{int64(len(args))}, nil
}

func (*recConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rec.add(q)
	rec.addArgs(args)
	rec.mu.Lock()
	res, ok := rec.results[q]
	rec.mu.Unlock()
	if !ok {
		return nil, errors.New("no result scripted for " + q)
	}
	return &recRows{res: res}, nil
}

type recRows struct {
	res recResult
	i   int
}

func (r *recRows) Columns() []string { return r.res.cols }
func (r *recRows) Close() error      { return nil }
func (r *recRows) Next(dest []driver.Value) error {
	if r.i >= len(r.res.rows) {
		return io.EOF
	}
	copy(dest, r.res.rows[r.i])
	r.i++
	return nil
}

type recDialect struct{ fakeDialect }

func (recDialect) DriverName() string                    { return "dygorec" }
func (recDialect) DSN(u string) (string, error)          { return u, nil }
func (recDialect) Placeholder(n int) string              { return "?" }
func (recDialect) Classify(err error) (sql.Kind, string) { return fakeDialect{}.Classify(err) }

func openRec(t *testing.T, results map[string]recResult) *sql.DB {
	t.Helper()
	return openRecWith(t, recDialect{}, results)
}

// recPG is a Postgres-like dialect: $n placeholders, RETURNING.
type recPG struct{ recDialect }

func (recPG) Placeholder(n int) string { return "$" + strconv.Itoa(n) }
func (recPG) BackslashEscapes() bool   { return false }
func (recPG) Quote(s string) string    { return `"` + s + `"` }
func (recPG) Returning() bool          { return true }

func openRecWith(t *testing.T, d sql.Dialect, results map[string]recResult) *sql.DB {
	t.Helper()
	rec.mu.Lock()
	rec.log, rec.results, rec.fails, rec.zero, rec.args = nil, results, map[string]int{}, map[string]bool{}, nil
	rec.mu.Unlock()
	db, err := sql.Open(context.Background(), d, sql.Config{URL: "rec://", MaxOpen: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type Base struct {
	ID int64 `db:"id"`
}

type user struct {
	Base
	Email     string
	FullName  string `db:"name"`
	CreatedAt time.Time
	Nick      sql.NullString
	Ignored   string `db:"-"`
}

var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func TestGetAndSelect(t *testing.T) {
	db := openRec(t, map[string]recResult{
		"SELECT one": {
			cols: []string{"id", "email", "name", "created_at", "nick"},
			rows: [][]driver.Value{{int64(1), "a@b.c", "Ann", now, nil}},
		},
		"SELECT many": {
			cols: []string{"id", "email"},
			rows: [][]driver.Value{{int64(1), "a@b.c"}, {int64(2), "b@b.c"}},
		},
		"SELECT none":  {cols: []string{"id"}},
		"SELECT count": {cols: []string{"n"}, rows: [][]driver.Value{{int64(42)}}},
		"SELECT bad":   {cols: []string{"id", "nope"}, rows: [][]driver.Value{{int64(1), "x"}}},
	})
	ctx := context.Background()

	var u user
	if err := db.Get(ctx, &u, "SELECT one"); err != nil {
		t.Fatal(err)
	}
	if u.ID != 1 || u.Email != "a@b.c" || u.FullName != "Ann" || !u.CreatedAt.Equal(now) || u.Nick.Valid {
		t.Fatalf("u = %+v", u)
	}

	var list []user
	if err := db.Select(ctx, &list, "SELECT many"); err != nil || len(list) != 2 || list[1].Email != "b@b.c" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	var ptrs []*user
	if err := db.Select(ctx, &ptrs, "SELECT many"); err != nil || len(ptrs) != 2 || ptrs[0].ID != 1 {
		t.Fatalf("ptrs = %+v, %v", ptrs, err)
	}
	var ids []int64
	if err := db.Select(ctx, &ids, "SELECT count"); err != nil || len(ids) != 1 || ids[0] != 42 {
		t.Fatalf("ids = %v, %v", ids, err)
	}
	var n int64
	if err := db.Get(ctx, &n, "SELECT count"); err != nil || n != 42 {
		t.Fatalf("n = %d, %v", n, err)
	}

	if err := db.Get(ctx, &u, "SELECT none"); !errors.Is(err, sql.ErrNotFound) {
		t.Fatalf("no row: %v", err)
	}
	empty := []user{{Email: "stale"}}
	if err := db.Select(ctx, &empty, "SELECT none"); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty = %#v, %v", empty, err)
	}
	if err := db.Get(ctx, &u, "SELECT bad"); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("unknown column: %v", err)
	}
	if err := db.Get(ctx, u, "SELECT one"); err == nil {
		t.Fatal("non-pointer dest: want error")
	}
	if err := db.Select(ctx, &u, "SELECT one"); err == nil {
		t.Fatal("non-slice dest: want error")
	}
	if err := db.Get(ctx, &n, "SELECT many"); err == nil {
		t.Fatal("two columns into int64: want error")
	}
}

func TestExecMapsErrors(t *testing.T) {
	db := openRec(t, nil)
	rec.fails["INSERT dup"] = 1
	_, err := db.Exec(context.Background(), "INSERT dup")
	if !errors.Is(err, sql.ErrDuplicate) {
		t.Fatalf("err = %v", err)
	}
	res, err := db.Exec(context.Background(), "UPDATE t SET a = ? WHERE id IN (?)", 1, sql.In([]int{1, 2}))
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 3 {
		t.Fatalf("args passed = %d, want 3", n)
	}
	if got := rec.take(); got[len(got)-1] != "UPDATE t SET a = ? WHERE id IN (?, ?)" {
		t.Fatalf("log = %q", got)
	}
}

func TestTxCommitRollbackAndSavepoints(t *testing.T) {
	db := openRec(t, nil)
	ctx := context.Background()

	if sql.InTx(ctx) {
		t.Fatal("InTx outside a transaction")
	}
	err := db.Tx(ctx, func(ctx context.Context) error {
		if !sql.InTx(ctx) {
			t.Error("InTx false inside Tx")
		}
		if _, err := db.Exec(ctx, "A"); err != nil {
			return err
		}
		inner := db.Tx(ctx, func(ctx context.Context) error {
			_, _ = db.Exec(ctx, "B")
			return errors.New("inner fails")
		})
		if inner == nil {
			t.Error("inner error lost")
		}
		return db.Tx(ctx, func(ctx context.Context) error {
			_, err := db.Exec(ctx, "C")
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"BEGIN", "A", "SAVEPOINT dygo_sp_1", "B", "ROLLBACK TO SAVEPOINT dygo_sp_1",
		"SAVEPOINT dygo_sp_2", "C", "RELEASE SAVEPOINT dygo_sp_2", "COMMIT"}
	if got := rec.take(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("log = %q\nwant  %q", got, want)
	}

	boom := errors.New("boom")
	if err := db.Tx(ctx, func(ctx context.Context) error { _, _ = db.Exec(ctx, "D"); return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got := rec.take(); strings.Join(got, "|") != "BEGIN|D|ROLLBACK" {
		t.Fatalf("log = %q", got)
	}
}

func TestTxPanicRollsBack(t *testing.T) {
	db := openRec(t, nil)
	defer func() {
		if p := recover(); p != "kaboom" {
			t.Fatalf("recovered %v", p)
		}
		if got := rec.take(); strings.Join(got, "|") != "BEGIN|ROLLBACK" {
			t.Fatalf("log = %q", got)
		}
	}()
	_ = db.Tx(context.Background(), func(context.Context) error { panic("kaboom") })
}

func TestTxRetries(t *testing.T) {
	db := openRec(t, nil)
	rec.fails["UPDATE retry"] = 2
	runs := 0
	err := db.TxWith(context.Background(), sql.TxOptions{Retries: 2, ReadOnly: true}, func(ctx context.Context) error {
		runs++
		_, err := db.Exec(ctx, "UPDATE retry")
		return err
	})
	if err != nil || runs != 3 {
		t.Fatalf("runs = %d, err = %v", runs, err)
	}
	if got := rec.take(); got[0] != "BEGIN READ ONLY" || got[len(got)-1] != "COMMIT" {
		t.Fatalf("log = %q", got)
	}

	rec.fails["UPDATE retry"] = 5
	runs = 0
	err = db.TxWith(context.Background(), sql.TxOptions{Retries: 1}, func(ctx context.Context) error {
		runs++
		_, err := db.Exec(ctx, "UPDATE retry")
		return err
	})
	if !errors.Is(err, sql.ErrRetryable) || runs != 2 {
		t.Fatalf("runs = %d, err = %v", runs, err)
	}
}

func TestTxDoneAndDetach(t *testing.T) {
	db := openRec(t, nil)
	var leaked context.Context
	_ = db.Tx(context.Background(), func(ctx context.Context) error {
		leaked = ctx
		d := sql.Detach(ctx)
		if sql.InTx(d) {
			t.Error("Detach kept the transaction")
		}
		return nil
	})
	_, err := db.Exec(leaked, "LATE")
	if !errors.Is(err, sql.ErrTxDone) || !strings.Contains(err.Error(), "LATE") {
		t.Fatalf("err = %v", err)
	}
	if _, err := db.Exec(sql.Detach(leaked), "DETACHED"); err != nil {
		t.Fatalf("detached: %v", err)
	}
	if err := db.Tx(leaked, func(context.Context) error { return nil }); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("savepoint in done tx: %v", err)
	}
}

func TestTxTwoPools(t *testing.T) {
	a := openRec(t, nil)
	b, err := sql.Open(context.Background(), recDialect{}, sql.Config{URL: "rec://b", MaxOpen: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	_ = a.Tx(context.Background(), func(ctx context.Context) error {
		return b.Tx(ctx, func(ctx context.Context) error {
			_, _ = a.Exec(ctx, "ON A")
			_, _ = b.Exec(ctx, "ON B")
			return nil
		})
	})
	// b's Tx inside a's is its own transaction, not a savepoint of a's.
	if got := strings.Join(rec.take(), "|"); got != "BEGIN|BEGIN|ON A|ON B|COMMIT|COMMIT" {
		t.Fatalf("log = %q", got)
	}
}

type orderController struct{ db *sql.DB }

func (c *orderController) Routes(r dygo.Router) {
	r.Post("/ok", dygo.HandleNoInput(func(ctx context.Context) (struct{}, error) {
		if !sql.InTx(ctx) {
			return struct{}{}, errors.New("not in a transaction")
		}
		_, err := c.db.Exec(ctx, "INSERT ok")
		return struct{}{}, err
	}), c.db.Transactional())
	r.Post("/fail", dygo.HandleNoInput(func(ctx context.Context) (struct{}, error) {
		_, err := c.db.Exec(ctx, "INSERT dup")
		return struct{}{}, err
	}), c.db.Transactional())
}

func TestTransactionalRoute(t *testing.T) {
	db := openRec(t, nil)
	root := &dygo.Module{
		Name:        "app",
		Providers:   []dygo.Provider{dygo.Value(db)},
		Controllers: []dygo.Provider{dygo.Provide(func(db *sql.DB) *orderController { return &orderController{db} })},
	}
	srv := dygotest.New(root).CompileHTTP(t)
	srv.POST("/ok").Do(t).Status(t, http.StatusCreated)
	if got := strings.Join(rec.take(), "|"); got != "BEGIN|INSERT ok|COMMIT" {
		t.Fatalf("log = %q", got)
	}
	rec.fails["INSERT dup"] = 1
	srv.POST("/fail").Do(t).Status(t, http.StatusConflict)
	if got := strings.Join(rec.take(), "|"); got != "BEGIN|INSERT dup|ROLLBACK" {
		t.Fatalf("log = %q", got)
	}
}
