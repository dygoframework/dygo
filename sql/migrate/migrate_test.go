package migrate

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dygoframework/dygo/sql"
)

func TestLoad(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_add_name.up.sql":         {Data: []byte("ALTER TABLE t ADD name TEXT;")},
		"0002_add_name.down.sql":       {Data: []byte("ALTER TABLE t DROP name;")},
		"0001_create_t.up.sql":         {Data: []byte("CREATE TABLE t (id INT);")},
		"0003_index.up.sql":            {Data: []byte("-- dygo:no-transaction\nCREATE INDEX CONCURRENTLY i ON t (id);")},
		"embed.go":                     {Data: []byte("package migrations")},
		"README.md":                    {Data: []byte("ignored")},
		"sub/0009_ignored.up.sql":      {Data: []byte("ignored")},
		"0010_empty_on_purpose.up.sql": {Data: []byte("")},
		"0011_contract.up.sql":         {Data: []byte("-- 0011: up.\n\n-- dygo:after-deploy\nALTER TABLE t DROP old;")},
		"0012_late_marker.up.sql":      {Data: []byte("SELECT 1;\n-- dygo:after-deploy\n")},
	}
	migs, err := Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range migs {
		got = append(got, m.String())
	}
	want := []string{"0001_create_t", "0002_add_name", "0003_index", "0010_empty_on_purpose", "0011_contract", "0012_late_marker"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if migs[0].HasDown || !migs[1].HasDown || !migs[2].UpNoTx || migs[1].UpNoTx {
		t.Fatalf("flags wrong: %+v", migs)
	}
	if migs[3].AfterDeploy || !migs[4].AfterDeploy || migs[5].AfterDeploy {
		t.Fatalf("after-deploy marker: %+v", migs[3:])
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"used twice": {"0001_a.up.sql": {}, "0001_b.up.sql": {}},
		"no up file": {"0001_a.down.sql": {}},
		"want NNNN":  {"create_users.sql": {}},
		"positive":   {"0000_zero.up.sql": {}},
	}
	for want, fsys := range cases {
		if _, err := Load(fsys); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
	if _, err := Load(nil); err == nil {
		t.Error("nil fs: no error")
	}
}

func TestSplit(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		mysql bool
		want  []string
	}{
		{"simple", "CREATE TABLE a (id INT);\nCREATE TABLE b (id INT);\n", false, []string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)"}},
		{"no trailing semicolon", "SELECT 1", false, []string{"SELECT 1"}},
		{"comments only", "-- nothing\n/* still ; nothing */\n;\n", false, nil},
		{"semicolon in string", "INSERT INTO a VALUES ('x;y'); SELECT 2", false, []string{"INSERT INTO a VALUES ('x;y')", "SELECT 2"}},
		{"doubled quote", "SELECT 'it''s;'; SELECT 3", false, []string{"SELECT 'it''s;'", "SELECT 3"}},
		{"line comment", "SELECT 1 -- a;b\n; SELECT 2", false, []string{"SELECT 1 -- a;b", "SELECT 2"}},
		{"dollar body", "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END $$ LANGUAGE plpgsql; SELECT 1", false,
			[]string{"CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END $$ LANGUAGE plpgsql", "SELECT 1"}},
		{"tagged dollar body", "DO $fn$ BEGIN PERFORM 1; END $fn$; SELECT $1", false, []string{"DO $fn$ BEGIN PERFORM 1; END $fn$", "SELECT $1"}},
		{"postgres E string", `SELECT E'a\';b'; SELECT 2`, false, []string{`SELECT E'a\';b'`, "SELECT 2"}},
		{"postgres plain backslash", `SELECT 'a\'; SELECT 2`, false, []string{`SELECT 'a\'`, "SELECT 2"}},
		{"mysql backslash", `INSERT INTO a VALUES ('a\';b'); SELECT 2`, true, []string{`INSERT INTO a VALUES ('a\';b')`, "SELECT 2"}},
		{"mysql hash comment", "SELECT 1 # x;y\n; SELECT 2", true, []string{"SELECT 1 # x;y", "SELECT 2"}},
		{"mysql backtick", "CREATE TABLE `a;b` (id INT); SELECT 2", true, []string{"CREATE TABLE `a;b` (id INT)", "SELECT 2"}},
		{"quoted identifier", `CREATE TABLE "a;b" (id INT); SELECT 2`, false, []string{`CREATE TABLE "a;b" (id INT)`, "SELECT 2"}},
	}
	for _, c := range cases {
		if got := Split(c.src, c.mysql); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestServerURL(t *testing.T) {
	cases := []struct{ kind, in, server, name string }{
		{"postgres", "postgres://u:p@localhost:5432/shop?sslmode=disable", "postgres://u:p@localhost:5432/postgres?sslmode=disable", "shop"},
		{"mysql", "mysql://u:p@localhost:3306/shop?tls=false", "mysql://u:p@localhost:3306/?tls=false", "shop"},
		{"mysql", "u:p@tcp(localhost:3306)/shop?parseTime=true", "u:p@tcp(localhost:3306)/?parseTime=true", "shop"},
	}
	for _, c := range cases {
		server, name, err := ServerURL(c.kind, c.in)
		if err != nil || server != c.server || name != c.name {
			t.Errorf("%s: got %q %q %v", c.in, server, name, err)
		}
	}
	for _, bad := range []struct{ kind, in string }{
		{"postgres", "host=localhost dbname=shop"},
		{"postgres", "postgres://localhost:5432/"},
		{"postgres", "postgres://localhost:5432/postgres"},
		{"mysql", "u:p@tcp(localhost:3306)/"},
	} {
		if _, _, err := ServerURL(bad.kind, bad.in); err == nil {
			t.Errorf("%s: no error", bad.in)
		}
	}
}

type fakeDialect struct{ sql.Dialect }

func (fakeDialect) Name() string { return "postgres" }

func runMainTest(t *testing.T, vars map[string]string, args ...string) (int, bool, string) {
	t.Helper()
	var out bytes.Buffer
	code, ok := Main(context.Background(), fakeDialect{}, fstest.MapFS{}, args, env(vars), Output(&out, &out))
	return code, ok, out.String()
}

func TestMainArgs(t *testing.T) {
	if _, ok, _ := runMainTest(t, nil); ok {
		t.Fatal("no args: handled")
	}
	if _, ok, _ := runMainTest(t, nil, "seed"); ok {
		t.Fatal("seed: handled")
	}
	vars := map[string]string{"DATABASE_URL": "postgres://u:p@localhost:1/x"}
	cases := []struct {
		args []string
		vars map[string]string
		code int
		want string
	}{
		{[]string{"db:help"}, nil, 0, "db:migrate"},
		{[]string{"db:nope"}, vars, 1, `unknown command "db:nope"`},
		{[]string{"db:migrate", "--bogus"}, vars, 1, "flag provided but not defined"},
		{[]string{"db:migrate", "extra"}, vars, 1, "unexpected argument"},
		{[]string{"db:status"}, map[string]string{}, 1, "DATABASE_URL"},
		{[]string{"db:drop"}, vars, 1, "pass --force"},
		{[]string{"db:schema:drop"}, vars, 1, "pass --force"},
		{[]string{"db:rollback"}, map[string]string{"DATABASE_URL": "postgres://x/y", "APP_ENV": "production"}, 1, "--allow-production"},
		{[]string{"db:drop", "--force"}, map[string]string{"DATABASE_URL": "postgres://x/y", "APP_ENV": "production"}, 1, "--allow-production"},
	}
	for _, c := range cases {
		code, ok, out := runMainTest(t, c.vars, c.args...)
		if !ok || code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("%v: code %d handled %v, output:\n%s\nwant %q", c.args, code, ok, out, c.want)
		}
	}
}
