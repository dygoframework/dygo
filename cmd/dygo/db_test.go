package main

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo/dygotest"
)

func TestSetEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := setEnv(path, "DATABASE_URL", "postgres://a", "# header\n"); err != nil {
		t.Fatal(err)
	}
	write(t, path, readFile(t, path)+"PORT=9900")
	if err := setEnv(path, "DATABASE_URL", "postgres://b", ""); err != nil {
		t.Fatal(err)
	}
	if err := setEnv(path, "LOG_LEVEL", "debug", ""); err != nil {
		t.Fatal(err)
	}
	want := "# header\n\n" + envComments["DATABASE_URL"] + "\nDATABASE_URL=postgres://b\nPORT=9900\nLOG_LEVEL=debug\n"
	if got := readFile(t, path); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if v, ok := envValue(path, "PORT"); !ok || v != "9900" {
		t.Fatalf("PORT = %q %v", v, ok)
	}
}

func TestDBSettingsURL(t *testing.T) {
	s := dbSettings{Driver: "postgres"}
	s.defaults("my-shop")
	if got := s.url(""); got != "postgres://app:app@localhost:5432/my_shop?sslmode=disable" {
		t.Errorf("postgres: %s", got)
	}
	if got := s.url("change-me"); got != "postgres://app:change-me@localhost:5432/my_shop?sslmode=disable" {
		t.Errorf("postgres example: %s", got)
	}
	m := dbSettings{Driver: "mysql", Host: "db", User: "root", Password: "p@ss"}
	m.defaults("blog")
	if got := m.url(""); got != "mysql://root:p%40ss@db:3306/blog" {
		t.Errorf("mysql: %s", got)
	}
	u := dbSettings{URL: "mysql://u:secret@h:3306/x"}
	if got := u.url("change-me"); got != "mysql://u:change-me@h:3306/x" {
		t.Errorf("url: %s", got)
	}
}

func TestPromptDB(t *testing.T) {
	var s dbSettings
	var out bytes.Buffer
	if err := promptDB(strings.NewReader("oracle\nmysql\n\n3307\nroot\n\nshop_dev\n"), &out, &s, "shop"); err != nil {
		t.Fatal(err)
	}
	want := dbSettings{Driver: "mysql", Host: "localhost", Port: 3307, User: "root", Password: "app", Name: "shop_dev"}
	if s != want {
		t.Fatalf("got %+v, want %+v", s, want)
	}
	if !strings.Contains(out.String(), "Please answer postgres or mysql") {
		t.Fatalf("prompt output:\n%s", out.String())
	}
}

func TestGenerateMigration(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	var out bytes.Buffer
	for _, name := range []string{"create_users", "AddUsersName"} {
		if err := generate(dir, "migration", name, genOptions{}, &out); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"0001_create_users.up.sql", "0001_create_users.down.sql", "0002_add_users_name.up.sql", "embed.go"} {
		if _, err := os.Stat(filepath.Join(dir, "migrations", f)); err != nil {
			t.Fatal(err)
		}
	}
	if err := generate(dir, "migration", "create-users", genOptions{}, &out); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate name: %v", err)
	}
	// Wider numbers already in the directory set the width.
	write(t, filepath.Join(dir, "migrations", "000041_wide.up.sql"), "")
	if err := generate(dir, "migration", "next", genOptions{}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "migrations", "000042_next.up.sql")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "dygo db init wires them") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestAddImportJoinsGroup(t *testing.T) {
	src := "package main\n\nimport (\n\t\"os\"\n\n\t\"github.com/dygoframework/dygo\" // core\n\n\t\"shop/src/config\"\n)\n"
	out, err := addImport("main.go", []byte(src), "github.com/dygoframework/dygo/sql/migrate")
	if err != nil {
		t.Fatal(err)
	}
	want := "\t\"github.com/dygoframework/dygo\" // core\n\t\"github.com/dygoframework/dygo/sql/migrate\"\n\n\t\"shop/src/config\"\n"
	if !strings.Contains(string(out), want) {
		t.Fatalf("got:\n%s", out)
	}
}

func TestDBCommandErrors(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"db"}, &out, &out); err != exitError(2) || !strings.Contains(out.String(), "dygo db <command>") {
		t.Fatalf("no args: %v\n%s", err, out.String())
	}
	for args, want := range map[string]string{
		"db nope":          `unknown command "nope"`,
		"db schema update": "want create or drop",
	} {
		if err := run(strings.Fields(args), &out, &out); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", args, err)
		}
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	write(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	t.Chdir(dir)
	if err := run([]string{"db", "status"}, &out, &out); err == nil || !strings.Contains(err.Error(), "run dygo db init first") {
		t.Fatalf("status without init: %v", err)
	}
	proj, err := findProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbInit(dir, proj, dbInitOptions{}, &out); err == nil || !strings.Contains(err.Error(), "-driver") {
		t.Fatalf("init without driver: %v", err)
	}
}

// TestNewDBProject builds projects with a database: dygo new -db for the
// modular layout, dygo db init for the simple one. They must vet and pass
// dygo check without a database. With DYGO_TEST_POSTGRES_URL and
// DYGO_TEST_MYSQL_URL set (as in CI), dygo db then creates, migrates and
// drops a real database through each app.
func TestNewDBProject(t *testing.T) {
	dygotest.Integration(t) // builds real programs
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	sfx := fmt.Sprint(time.Now().UnixNano())

	t.Run("modular postgres", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "shop")
		var out bytes.Buffer
		if err := run([]string{"new", dir, "-t", "modular", "-db", "postgres", "-replace", repo}, &out, &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		t.Chdir(dir)
		for _, want := range []string{"CREATE shop/compose.yaml", "CREATE shop/migrations/embed.go", "docker compose up -d"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q in\n%s", want, out.String())
			}
		}
		if got := readFile(t, ".env"); !strings.Contains(got, "DATABASE_URL=postgres://app:app@localhost:5432/shop?sslmode=disable") {
			t.Errorf(".env:\n%s", got)
		}
		if got := readFile(t, ".env.example"); !strings.Contains(got, "app:change-me@") || strings.Contains(got, "DATABASE_URL=memory://") {
			t.Errorf(".env.example:\n%s", got)
		}
		if got := readFile(t, "src/database/database.go"); !strings.Contains(got, "postgres.Module(sql.NoHealth())") {
			t.Errorf("database.go:\n%s", got)
		}
		dbRoundTrip(t, os.Getenv("DYGO_TEST_POSTGRES_URL"), "dygo_cli_pg_"+sfx)
	})

	t.Run("simple mysql", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "blog")
		var out bytes.Buffer
		if err := run([]string{"new", dir, "-replace", repo}, &out, &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		t.Chdir(dir)
		out.Reset()
		if err := run([]string{"db", "init", "-driver", "mysql", "-name", "blog_dev"}, &out, &out); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		for _, want := range []string{"UPDATE app.go (sql.Module(nil) -> mysql.Module())", "UPDATE main.go (db:* commands)", "CREATE migrations/embed.go"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q in\n%s", want, out.String())
			}
		}
		// Again with other settings: refused, then accepted with -force.
		if err := run([]string{"db", "init", "-driver", "mysql", "-name", "other", "-skip-tidy"}, &out, &out); err == nil || !strings.Contains(err.Error(), "-force") {
			t.Fatalf("second init: %v", err)
		}
		if err := run([]string{"db", "init", "-driver", "mysql", "-name", "blog_dev", "-skip-tidy"}, &out, &out); err != nil {
			t.Fatalf("same init again: %v", err)
		}
		if strings.Count(readFile(t, "main.go"), "migrate.Main(") != 1 || strings.Count(readFile(t, "app.go"), "mysql.Module()") != 1 {
			t.Fatal("a second init wired twice")
		}
		dbRoundTrip(t, os.Getenv("DYGO_TEST_MYSQL_URL"), "dygo_cli_my_"+sfx)
	})
}

// dbRoundTrip checks the project in the current directory, then runs the
// dygo db commands against a new database named name on the server in
// serverURL, when it is set.
func dbRoundTrip(t *testing.T, serverURL, name string) {
	t.Helper()
	var out bytes.Buffer
	if err := run([]string{"g", "migration", "create_items"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	up := filepath.Join("migrations", "0001_create_items.up.sql")
	write(t, up, readFile(t, up)+"CREATE TABLE items (id BIGINT PRIMARY KEY, name VARCHAR(50) NOT NULL);\nINSERT INTO items VALUES (1, 'a;b');\n")
	down := filepath.Join("migrations", "0001_create_items.down.sql")
	write(t, down, readFile(t, down)+"DROP TABLE items;\n")
	if b, err := exec.Command("go", "vet", "./...").CombinedOutput(); err != nil {
		t.Fatalf("go vet: %v\n%s", err, b)
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"check"}, &stdout, &stderr); err != nil {
		t.Fatalf("check: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	if serverURL == "" {
		t.Log("no test database URL; skipping dygo db against a server")
		return
	}
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	if err := setEnv(".env", "DATABASE_URL", u.String(), ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run([]string{"db", "drop", "--force", "--if-exists"}, &out, &out) })
	expect := func(code int, want string, args ...string) {
		t.Helper()
		var b bytes.Buffer
		err := run(append([]string{"db"}, args...), &b, &b)
		got := 0
		if err != nil {
			got = 1
			if e, ok := err.(exitError); ok {
				got = int(e)
			}
		}
		if got != code || !strings.Contains(b.String(), want) {
			t.Fatalf("dygo db %v: exit %d (%v), output:\n%s\nwant exit %d and %q", args, got, err, b.String(), code, want)
		}
	}
	expect(1, "dygo db create", "status")
	expect(0, "Created database "+name, "create")
	expect(1, "1 pending", "status", "--check")
	expect(0, "UP   0001_create_items", "migrate")
	expect(0, "1 applied, 0 pending", "status")
	expect(0, "DOWN 0001_create_items", "rollback")
	expect(0, "UP   0001_create_items", "schema", "create")
	entityMigrations(t, expect)
	expect(0, "Dropped 4 tables", "schema", "drop", "--force", "--all")
	expect(0, "Dropped database "+name, "drop", "--force")
}

// entityMigrations fills migrations from an entity and runs them: create
// the table, add a field, drop a column the entity does not have, then
// roll all three back.
func entityMigrations(t *testing.T, expect func(code int, want string, args ...string)) {
	t.Helper()
	gen := func(want string, args ...string) {
		t.Helper()
		var b bytes.Buffer
		if err := run(append([]string{"g"}, args...), &b, &b); err != nil || !strings.Contains(b.String(), want) {
			t.Fatalf("dygo g %v: %v\n%s\nwant %q", args, err, b.String(), want)
		}
	}
	gen("For the table: dygo g migration", "entity", "Product", "-fields", "title:string price:float note:text? publishedAt:time? active:bool")
	gen("CREATE TABLE products", "migration", "create_products")
	expect(0, "UP   0002_create_products", "migrate")
	gen("For the table: dygo g migration", "entity", "Product", "-fields", "stock:int")
	gen("ALTER TABLE products: add stock", "migration", "add_stock_to_products")
	if up := readFile(t, filepath.Join("migrations", "0003_add_stock_to_products.up.sql")); !strings.Contains(up, "ADD COLUMN stock BIGINT NOT NULL DEFAULT 0;") {
		t.Fatalf("0003 up:\n%s", up)
	}
	write(t, filepath.Join("migrations", "0004_legacy.up.sql"), "ALTER TABLE products ADD COLUMN legacy VARCHAR(20) NULL;\n")
	write(t, filepath.Join("migrations", "0004_legacy.down.sql"), "ALTER TABLE products DROP COLUMN legacy;\n")
	expect(0, "UP   0004_legacy", "migrate")
	gen("ALTER TABLE products: drop legacy", "migration", "update_products")
	if down := readFile(t, filepath.Join("migrations", "0005_update_products.down.sql")); !strings.Contains(down, "ADD COLUMN legacy VARCHAR(20) NULL;") {
		t.Fatalf("0005 down:\n%s", down)
	}
	gen("Every table already matches its entity", "migration", "change_products")
	expect(0, "UP   0005_update_products", "migrate")
	expect(0, "DOWN 0002_create_products", "rollback", "--steps", "5")
	expect(0, "UP   0006_change_products", "migrate")

	// Rename note to description and make price an integer, in the entity
	// and its bodies, then let the generator write the SQL.
	file := filepath.Join(filepath.Dir(findEntityFile(t, "Product")), "products.go")
	src := readFile(t, file)
	src = strings.NewReplacer("Note", "Description", `"note`, `"description`, "float64", "int64").Replace(src)
	write(t, file, src)
	gen("to BIGINT loses data: decimals are rounded", "migration", "alter_products", "-rename", "note:description")
	up := readFile(t, filepath.Join("migrations", "0007_alter_products.up.sql"))
	for _, want := range []string{"RENAME COLUMN note TO description;", "price", "-- DATA LOSS: decimals are rounded"} {
		if !strings.Contains(up, want) {
			t.Fatalf("0007 up misses %q:\n%s", want, up)
		}
	}
	expect(0, "UP   0007_alter_products", "migrate")
	expect(0, "DOWN 0007_alter_products", "rollback")
	expect(0, "UP   0007_alter_products", "migrate")
	gen("Every table already matches its entity", "migration", "modify_products")

	// -safe: rename description to summary in two steps. Rows written
	// through the old column and through the new one between the steps must
	// end up in summary; a CHECK constraint fails the contract if not.
	src = strings.NewReplacer("Description", "Summary", `"description`, `"summary`).Replace(readFile(t, file))
	write(t, file, src)
	gen("Next: review the four files", "migration", "rename_description_in_products", "-safe", "-rename", "description:summary")
	expandUp := filepath.Join("migrations", "0009_rename_description_in_products.up.sql")
	contractUp := filepath.Join("migrations", "0010_rename_description_in_products_contract.up.sql")
	contractDown := filepath.Join("migrations", "0010_rename_description_in_products_contract.down.sql")
	if !strings.HasPrefix(readFile(t, contractUp), "-- dygo:after-deploy\n") {
		t.Fatalf("contract up:\n%s", readFile(t, contractUp))
	}
	const row = ", 't', 1, '2026-01-01 00:00:00', TRUE);\n"
	write(t, expandUp, readFile(t, expandUp)+
		"INSERT INTO products (id, description, title, price, published_at, active) VALUES (1001, 'from-old'"+row+
		"INSERT INTO products (id, summary, title, price, published_at, active) VALUES (1002, 'from-new'"+row+
		"UPDATE products SET description = 'old-upd' WHERE id = 1002;\n"+
		"UPDATE products SET summary = 'new-upd' WHERE id = 1001;\n")
	write(t, contractUp, readFile(t, contractUp)+
		"CREATE TABLE sync_check (v VARCHAR(20) NOT NULL, CHECK (v = 'ok'));\n"+
		"INSERT INTO sync_check SELECT CASE WHEN summary = 'new-upd' THEN 'ok' ELSE 'bad 1001' END FROM products WHERE id = 1001;\n"+
		"INSERT INTO sync_check SELECT CASE WHEN summary = 'old-upd' THEN 'ok' ELSE 'bad 1002' END FROM products WHERE id = 1002;\n")
	write(t, contractDown, readFile(t, contractDown)+"DROP TABLE sync_check;\n")
	expandDown := filepath.Join("migrations", "0009_rename_description_in_products.down.sql")
	write(t, expandDown, readFile(t, expandDown)+"DELETE FROM products WHERE id IN (1001, 1002);\n")
	expect(0, "Waiting: 0010_rename_description_in_products_contract runs after the deploy", "migrate")
	expect(0, "pending, after deploy (--contract)", "status", "--check")
	expect(0, "UP   0010_rename_description_in_products_contract", "migrate", "--contract")
	expect(0, "DOWN 0010_rename_description_in_products_contract", "rollback")
	expect(0, "DOWN 0009_rename_description_in_products", "rollback")
	expect(0, "UP   0010_rename_description_in_products_contract", "migrate", "--contract")
	gen("Every table already matches its entity", "migration", "check_sync_of_products")
}

// findEntityFile returns the file that declares the entity under the
// current directory.
func findEntityFile(t *testing.T, entity string) string {
	t.Helper()
	entities, err := listEntities(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entities {
		if e.Entity == entity {
			return e.File
		}
	}
	t.Fatalf("no entity %s", entity)
	return ""
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestMissingArguments: a missing or extra argument gets a one-line error
// naming the command and an example, not the whole usage.
func TestMissingArguments(t *testing.T) {
	for args, want := range map[string]string{
		"g module":   "dygo g module: missing name (example: dygo g module billing)",
		"g res":      "dygo g res: missing name (example: dygo g res products)",
		"generate":   "dygo generate: missing kind and name",
		"g s a b":    `dygo g s: unexpected argument "b"`,
		"g bogus x":  `unknown kind "bogus"`,
		"new":        "dygo new: missing project name",
		"new a b":    `dygo new: unexpected argument "b"`,
		"routes a b": `unexpected argument "b"`,
	} {
		var out bytes.Buffer
		err := run(strings.Fields(args), &out, &out)
		if err == nil || err == errUsage || !strings.Contains(err.Error(), want) {
			t.Errorf("dygo %s: got %v, want %q", args, err, want)
		}
	}
	for kind := range kinds {
		if kindExamples[kinds[kind]] == "" {
			t.Errorf("kind %s has no example name", kind)
		}
	}
}

// TestHelp: dygo and dygo help list the commands in one line each; the
// details come from dygo help <command> and dygo <command> -h.
func TestHelp(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"help"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(out.String(), "\n"); lines > 25 || strings.Contains(out.String(), "-fields") {
		t.Fatalf("overview is not short (%d lines):\n%s", lines, out.String())
	}
	for name := range commandHelp {
		if !strings.Contains(out.String(), "\n  "+name+" ") {
			t.Errorf("overview does not list %s", name)
		}
	}
	for args, want := range map[string]string{
		"help generate":  "-fields",
		"help g":         "migration (mig)",
		"g -h":           "-dry-run",
		"g res x --help": "Kinds:",
		"new -help":      "-template",
		"routes -h":      "DYGO_INSPECT_OUT",
		"help db":        "schema drop --force",
		"db -h":          "schema drop --force",
	} {
		out.Reset()
		if err := run(strings.Fields(args), &out, &out); err != nil || !strings.Contains(out.String(), want) {
			t.Errorf("dygo %s: %v, want %q in\n%s", args, err, want, out.String())
		}
	}
	if err := run([]string{"help", "nope"}, &out, &out); err == nil || !strings.Contains(err.Error(), `unknown command "nope"`) {
		t.Errorf("help nope: %v", err)
	}
	if err := run([]string{"g", "res", "x", "-bogus"}, &out, &out); err == nil || !strings.Contains(err.Error(), "see dygo help generate") {
		t.Errorf("bad flag: %v", err)
	}
}
