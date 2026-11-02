package migrate_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/sql"
	"github.com/dygoframework/dygo/sql/migrate"
	"github.com/dygoframework/dygo/sql/mysql"
	"github.com/dygoframework/dygo/sql/postgres"
)

// The integration tests run against the databases CI starts (see
// .github/workflows/ci.yml) and skip without them:
//
//	DYGO_TEST_POSTGRES_URL=postgres://postgres:dygo@localhost:5432/dygo?sslmode=disable
//	DYGO_TEST_MYSQL_URL=mysql://root:dygo@127.0.0.1:3306/dygo

func TestPostgres(t *testing.T) { runIntegration(t, postgres.Dialect(), "DYGO_TEST_POSTGRES_URL") }
func TestMySQL(t *testing.T)    { runIntegration(t, mysql.Dialect(), "DYGO_TEST_MYSQL_URL") }

func runIntegration(t *testing.T, d sql.Dialect, envName string) {
	raw := dygotest.RequireEnv(t, envName)
	ctx := context.Background()
	db, err := sql.Open(ctx, d, sql.Config{URL: raw, MaxOpen: 4, ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sfx := fmt.Sprintf("%d", time.Now().UnixNano())
	parent, child, table := "mig_parent_"+sfx, "mig_child_"+sfx, "dygo_migrations_"+sfx
	cleanup := func() {
		for _, q := range []string{"DROP TABLE IF EXISTS " + child, "DROP TABLE IF EXISTS " + parent, "DROP TABLE IF EXISTS " + table} {
			_, _ = db.SQL().ExecContext(ctx, q)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	files := fstest.MapFS{
		"0001_create_parent.up.sql":   {Data: []byte(fmt.Sprintf("CREATE TABLE %s (id BIGINT PRIMARY KEY, email VARCHAR(100) NOT NULL);", parent))},
		"0001_create_parent.down.sql": {Data: []byte("DROP TABLE " + parent + ";")},
		// Two statements and a string with a semicolon: MySQL needs the split.
		"0002_create_child.up.sql": {Data: []byte(fmt.Sprintf(
			"-- children\nCREATE TABLE %s (id BIGINT PRIMARY KEY, parent_id BIGINT NOT NULL REFERENCES %s (id));\n"+
				"INSERT INTO %s (id, email) VALUES (1, 'a;b@example.com');\n", child, parent, parent))},
		"0002_create_child.down.sql": {Data: []byte(fmt.Sprintf("DELETE FROM %s;\nDROP TABLE %s;", parent, child))},
		"0003_add_name.up.sql":       {Data: []byte(fmt.Sprintf("ALTER TABLE %s ADD COLUMN name VARCHAR(50);", parent))},
		"0003_add_name.down.sql":     {Data: []byte(fmt.Sprintf("ALTER TABLE %s DROP COLUMN name;", parent))},
	}
	newM := func(fsys fstest.MapFS, opts ...migrate.Option) *migrate.Migrator {
		t.Helper()
		m, err := migrate.New(db, fsys, append([]migrate.Option{migrate.Table(table)}, opts...)...)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	versions := func(migs []migrate.Migration) string {
		var s []string
		for _, m := range migs {
			s = append(s, fmt.Sprint(m.Version))
		}
		return strings.Join(s, ",")
	}

	t.Run("up, status, down", func(t *testing.T) {
		m := newM(files)
		pending, err := m.Pending(ctx, 2)
		if err != nil || versions(pending) != "1,2" {
			t.Fatalf("pending = %v, %v", versions(pending), err)
		}
		done, err := m.Up(ctx, 0)
		if err != nil || versions(done) != "1,2,3" {
			t.Fatalf("up = %v, %v", versions(done), err)
		}
		var email string
		if err := db.Get(ctx, &email, "SELECT email FROM "+parent+" WHERE id = ?", 1); err != nil || email != "a;b@example.com" {
			t.Fatalf("email = %q, %v", email, err)
		}
		if done, err := m.Up(ctx, 0); err != nil || len(done) != 0 {
			t.Fatalf("second up = %v, %v", versions(done), err)
		}
		states, err := m.Status(ctx)
		if err != nil || len(states) != 3 || !states[2].Applied || states[2].AppliedAt.IsZero() {
			t.Fatalf("status = %+v, %v", states, err)
		}
		if done, err := m.Down(ctx, 1); err != nil || versions(done) != "3" {
			t.Fatalf("down = %v, %v", versions(done), err)
		}
		if done, err := m.DownTo(ctx, 0); err != nil || versions(done) != "2,1" {
			t.Fatalf("down to 0 = %v, %v", versions(done), err)
		}
		if _, err := db.SQL().ExecContext(ctx, "SELECT 1 FROM "+parent); err == nil {
			t.Fatal("parent table still exists")
		}
	})

	t.Run("failure", func(t *testing.T) {
		bad := fstest.MapFS{
			"0001_create_parent.up.sql": files["0001_create_parent.up.sql"],
			"0002_broken.up.sql":        {Data: []byte(fmt.Sprintf("CREATE TABLE %s (id INT);\nNOT SQL AT ALL;", child))},
		}
		m := newM(bad)
		done, err := m.Up(ctx, 0)
		if err == nil || !strings.Contains(err.Error(), "0002_broken (up)") || versions(done) != "1" {
			t.Fatalf("up = %v, %v", versions(done), err)
		}
		states, _ := m.Status(ctx)
		if len(states) != 2 || states[1].Applied {
			t.Fatalf("status = %+v", states)
		}
		if d.Name() == "postgres" { // transactional DDL: the CREATE TABLE was rolled back
			if _, err := db.SQL().ExecContext(ctx, "SELECT 1 FROM "+child); err == nil {
				t.Fatal("child table exists after a failed migration")
			}
		}
		cleanup()
	})

	t.Run("out of order", func(t *testing.T) {
		gap := fstest.MapFS{}
		for k, v := range files {
			if !strings.HasPrefix(k, "0002") {
				gap[k] = v
			}
		}
		if _, err := newM(gap).Up(ctx, 0); err != nil {
			t.Fatal(err)
		}
		_, err := newM(files).Up(ctx, 0)
		if err == nil || !strings.Contains(err.Error(), "out-of-order") {
			t.Fatalf("err = %v", err)
		}
		if done, err := newM(files, migrate.OutOfOrder()).Up(ctx, 0); err != nil || versions(done) != "2" {
			t.Fatalf("up = %v, %v", versions(done), err)
		}
		cleanup()
	})

	t.Run("after deploy", func(t *testing.T) {
		held := fstest.MapFS{}
		for k, v := range files {
			held[k] = v
		}
		held["0002_create_child.up.sql"] = &fstest.MapFile{Data: append([]byte("-- 0002: contract\n-- dygo:after-deploy\n"), files["0002_create_child.up.sql"].Data...)}
		m := newM(held)
		if done, err := m.Up(ctx, 0); err != nil || versions(done) != "1" {
			t.Fatalf("up = %v, %v", versions(done), err)
		}
		if mig, ok := m.Held(); !ok || mig.Version != 2 {
			t.Fatalf("held = %v, %v", mig, ok)
		}
		if pending, err := m.Pending(ctx, 0); err != nil || len(pending) != 0 {
			t.Fatalf("pending = %v, %v", versions(pending), err)
		}
		states, _ := m.Status(ctx)
		if !states[1].AfterDeploy || states[1].Applied || states[2].Applied {
			t.Fatalf("status = %+v", states)
		}
		if done, err := newM(held, migrate.Contract()).Up(ctx, 0); err != nil || versions(done) != "2,3" {
			t.Fatalf("up --contract = %v, %v", versions(done), err)
		}
		if _, ok := m.Held(); !ok { // Held is per call: a new Pending clears it
			t.Fatal("held cleared too early")
		}
		if _, err := m.Pending(ctx, 0); err != nil {
			t.Fatal(err)
		}
		if _, ok := m.Held(); ok {
			t.Fatal("held after everything was applied")
		}
		cleanup()
	})

	t.Run("concurrent", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make([]error, 3)
		count := make([]int, 3)
		for i := range 3 {
			wg.Go(func() {
				done, err := newM(files).Up(ctx, 0)
				errs[i], count[i] = err, len(done)
			})
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
		if count[0]+count[1]+count[2] != 3 {
			t.Fatalf("applied %v migrations in total, want 3", count)
		}
		cleanup()
	})

	t.Run("on boot", func(t *testing.T) {
		root := &dygo.Module{Name: "app", Imports: []*dygo.Module{
			sql.Module(d, sql.WithConfig(sql.Config{URL: raw}), sql.NoHealth()),
			migrate.OnBoot(files, migrate.Table(table)),
		}}
		app, err := dygo.CreateApplicationContext(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		applied, err := dygo.Get[migrate.Applied](app)
		_ = app.Close(ctx)
		if err != nil || versions(applied) != "1,2,3" {
			t.Fatalf("applied = %v, %v", versions(applied), err)
		}
		cleanup()
	})

	t.Run("main", func(t *testing.T) {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		name := "dygo_main_" + sfx
		u.Path = "/" + name
		cfg := sql.Config{URL: u.String()}
		run := func(args ...string) (int, string) {
			var out bytes.Buffer
			code, ok := migrate.Main(ctx, d, files, args, migrate.WithConfig(cfg), migrate.Files(), migrate.Output(&out, &out))
			if !ok {
				t.Fatalf("%v not handled", args)
			}
			return code, out.String()
		}
		expect := func(code int, want string, args ...string) {
			t.Helper()
			got, out := run(args...)
			if got != code || !strings.Contains(out, want) {
				t.Fatalf("%v: exit %d, output:\n%s\nwant exit %d and %q", args, got, out, code, want)
			}
		}
		expect(0, "Created database "+name, "db:create")
		t.Cleanup(func() { run("db:drop", "--force", "--if-exists") })
		expect(1, "already exists", "db:create")
		expect(0, "already exists", "db:create", "--if-not-exists")
		expect(1, "3 pending", "db:status", "--check")
		expect(0, "0002_create_child", "db:migrate", "--dry-run")
		expect(0, "UP   0001_create_parent", "db:schema:create")
		expect(1, "already has tables", "db:schema:create")
		expect(0, "3 applied, 0 pending", "db:status", "--check")
		expect(0, "Nothing to migrate", "db:migrate")
		expect(0, "DOWN 0003_add_name", "db:rollback")
		expect(0, "DOWN 0002_create_child", "db:rollback", "--to", "1")
		expect(0, "UP   0003_add_name", "db:migrate")
		expect(0, "DOWN 0001_create_parent", "db:schema:drop", "--force")
		expect(0, "UP   0001", "db:migrate", "--to", "1")
		expect(0, "Dropped 2 tables", "db:schema:drop", "--force", "--all")
		expect(0, "Dropped database "+name, "db:drop", "--force")
		expect(0, "does not exist", "db:drop", "--force", "--if-exists")
	})
}
