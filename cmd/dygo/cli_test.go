package main

import (
	"bytes"
	"flag"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/dygotest"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

const rootModule = `package main

import "github.com/dygoframework/dygo"

// AppModule is the root module.
func AppModule() *dygo.Module {
	return &dygo.Module{
		Name: "app",
	}
}
`

// TestGenerateGolden generates every kind into a small project and compares
// the whole tree, including the edited module files, with testdata/golden.
func TestGenerateGolden(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	write(t, filepath.Join(dir, "app.go"), rootModule)
	var out bytes.Buffer
	for _, g := range []struct{ kind, name, fields string }{
		{"resource", "order-items", ""},
		{"entity", "order-items", "qty:int note:text? contact:email due:time"},
		{"resource", "products", "title:string price:float tags:text? owner:email?"},
		{"entity", "Invoice", "number:string paid:bool owner:email"},
		{"crud", "Invoice", ""},
		{"module", "auth", ""},
		{"service", "auth", ""},
		{"controller", "auth", ""},
		{"service", "auth/tokens", ""},
		{"service", "mailer", ""},
		{"command", "seed-data", ""},
		{"auth", "account", ""},
		{"listener", "notify", ""},
		{"cron", "reports", ""},
		{"job", "welcome-email", ""},
		{"gateway", "chat", ""},
		{"rpc", "users-rpc", ""},
	} {
		fields, err := parseFields(g.fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := generate(dir, g.kind, g.name, genOptions{Fields: fields}, &out); err != nil {
			t.Fatalf("%s %s: %v", g.kind, g.name, err)
		}
	}
	compareGolden(t, dir, out.String(), filepath.Join("testdata", "golden"))

	// Generating again refuses to overwrite.
	if err := generate(dir, "resource", "order-items", genOptions{}, &out); err == nil {
		t.Fatal("want an error for an existing resource")
	}
	if err := generate(dir, "service", "mailer", genOptions{}, &out); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing service: %v", err)
	}
}

// compareGolden compares the files in dir and the command output with the
// golden files in golden, or rewrites them with -update.
func compareGolden(t *testing.T, dir, output, golden string) {
	t.Helper()
	got := map[string]string{"output.txt": output}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "go.mod" {
			return err
		}
		data, err := os.ReadFile(path)
		got[filepath.ToSlash(rel(dir, path))] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.RemoveAll(golden); err != nil {
			t.Fatal(err)
		}
		for name, data := range got {
			write(t, filepath.Join(golden, filepath.FromSlash(name)+".golden"), data)
		}
		return
	}
	want := map[string]string{}
	err = filepath.WalkDir(golden, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		want[strings.TrimSuffix(filepath.ToSlash(rel(golden, path)), ".golden")] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s differs from the golden file (go test -update to accept):\n%s", name, got[name])
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected file %s (go test -update to accept)", name)
		}
	}

}

// TestGenerateSecurityGolden generates dygo g security into a small project
// and compares the tree with testdata/security.
func TestGenerateSecurityGolden(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	write(t, filepath.Join(dir, "app.go"), rootModuleWithMiddleware)
	write(t, filepath.Join(dir, ".env.example"), "PORT=9900\n")
	write(t, filepath.Join(dir, "dygo.json"), "{\n  \"main\": \".\"\n}\n")
	var out bytes.Buffer
	if err := generate(dir, "security", "security", genOptions{}, &out); err != nil {
		t.Fatal(err)
	}
	// Generators in a security project write protected routes and tests that sign in.
	for _, g := range []struct {
		kind, name string
		o          genOptions
	}{
		{"resource", "products", genOptions{}},
		{"resource", "posts", genOptions{PublicRead: true, WriteRole: "-"}},
		{"controller", "billing", genOptions{}},
		{"controller", "hooks", genOptions{Public: true}},
	} {
		if err := generate(dir, g.kind, g.name, g.o, &out); err != nil {
			t.Fatalf("%s %s: %v", g.kind, g.name, err)
		}
	}
	compareGolden(t, dir, out.String(), filepath.Join("testdata", "security"))

	// An app uses g auth or g security, not both.
	if err := generate(dir, "auth", "auth", genOptions{}, &out); err == nil || !strings.Contains(err.Error(), "made by dygo g security") {
		t.Fatalf("g auth after g security: %v", err)
	}
	plain := t.TempDir()
	write(t, filepath.Join(plain, "go.mod"), "module example.com/plain\n\ngo 1.27.0\n")
	write(t, filepath.Join(plain, "app.go"), rootModule)
	if err := generate(plain, "controller", "x", genOptions{Public: true}, &out); err == nil || !strings.Contains(err.Error(), "need a security project") {
		t.Fatalf("-public outside a security project: %v", err)
	}
	other := t.TempDir()
	write(t, filepath.Join(other, "go.mod"), "module example.com/other\n\ngo 1.27.0\n")
	write(t, filepath.Join(other, "app.go"), rootModule)
	if err := generate(other, "auth", "auth", genOptions{}, &out); err != nil {
		t.Fatal(err)
	}
	if err := generate(other, "security", "security", genOptions{}, &out); err == nil || !strings.Contains(err.Error(), "To switch to dygo g security") {
		t.Fatalf("g security after g auth: %v", err)
	}
}

// TestGenerateSecurityOIDCGolden generates dygo g security -oidc and
// compares the tree with testdata/security-oidc.
func TestGenerateSecurityOIDCGolden(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	write(t, filepath.Join(dir, "app.go"), rootModuleWithMiddleware)
	write(t, filepath.Join(dir, ".env.example"), "PORT=9900\n")
	var out bytes.Buffer
	if err := generate(dir, "security", "security", genOptions{OIDC: true}, &out); err != nil {
		t.Fatal(err)
	}
	compareGolden(t, dir, out.String(), filepath.Join("testdata", "security-oidc"))
	if err := run([]string{"g", "co", "x", "-oidc"}, &out, &out); err == nil || !strings.Contains(err.Error(), "-oidc is for dygo g security") {
		t.Fatalf("-oidc on a controller: %v", err)
	}
}

const rootModuleWithMiddleware = `package main

import (
	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/middleware"
)

// AppModule is the root module.
func AppModule() *dygo.Module {
	return &dygo.Module{
		Name: "app",
	}
}

// Options are the application options.
func Options() []dygo.Option {
	return []dygo.Option{
		dygo.WithMiddleware(
			middleware.RequestID(),
		),
	}
}
`

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestNewProject is the end to end check of the CLI: dygo new, then generate a
// resource, and the project vets and passes its tests. The inspection commands
// run against it too. It builds real programs, so -short skips it. Features
// generated from the root go to src/modules and are imported in app.module.go.
func TestNewProject(t *testing.T) {
	dygotest.Integration(t) // builds real programs
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "demo")
	var out bytes.Buffer
	if err := run([]string{"new", dir, "-replace", repo}, &out, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	t.Chdir(dir)
	if env := readFile(t, ".env"); !strings.Contains(env, "PORT=9900") || !strings.HasPrefix(env, "# .env: settings") || !strings.HasPrefix(readFile(t, ".env.example"), "# .env.example: every setting") {
		t.Fatalf(".env:\n%s", env)
	}
	for _, args := range [][]string{
		{"g", "res", "users", "-fields", "name:string email:email"},
		{"g", "entity", "users", "-fields", "age:int? born:time?"},
		{"g", "s", "mailer"},
		{"g", "command", "seed"},
		{"g", "auth"},
		{"g", "test", "users"},
		{"g", "listener", "notify"},
		{"g", "cron", "reports"},
		{"g", "job", "welcome-email"},
		{"g", "rpc", "users-rpc"},
		{"g", "entity", "Invoice", "-fields", "number:string owner:email"},
		{"g", "crud", "Invoice"},
		{"g", "mo", "billing"},
		{"g", "s", "billing"},
		{"g", "co", "billing"},
	} {
		if err := run(args, &out, &out); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
	}
	for _, f := range []string{"users.go", "module.go", "service.go", "controller.go", "controller_test.go", "routes_test.go"} {
		if _, err := os.Stat(filepath.Join("src", "modules", "users", f)); err != nil {
			t.Errorf("want src/modules/users/%s: %v\n%s", f, err, out.String())
		}
	}
	for _, f := range []string{"src/modules/mailer/service.go", "src/modules/seed/command.go", "src/modules/auth/module.go"} {
		if _, err := os.Stat(filepath.FromSlash(f)); err != nil {
			t.Errorf("want %s: %v\n%s", f, err, out.String())
		}
	}
	app, _ := os.ReadFile("app.module.go")
	billing, _ := os.ReadFile(filepath.Join("src", "modules", "billing", "module.go"))
	for _, want := range []string{"users.Module", "invoices.Module", "billing.Module", "mailer.NewService", "seed.NewCommand"} {
		if !strings.Contains(string(app), want) {
			t.Errorf("app.module.go: missing %s\n%s", want, app)
		}
	}
	if !strings.Contains(string(billing), "dygo.Provide(NewService)") || !strings.Contains(string(billing), "dygo.Provide(NewController)") {
		t.Errorf("billing service and controller belong in BillingModule:\n%s", billing)
	}
	for _, args := range [][]string{{"vet", "./..."}, {"test", "./..."}, {"run", ".", "seed", "x"}} {
		cmd := exec.Command("go", args...)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, b)
		}
		if args[0] == "run" && !strings.Contains(string(b), "seed: args [x]") {
			t.Fatalf("go run . seed:\n%s", b)
		}
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"routes"}, &stdout, &stderr); err != nil {
		t.Fatalf("routes: %v\n%s", err, stderr.String())
	}
	for _, want := range []string{"GET     /users", "DELETE  /users/{id}", "users.CreateIn", "\n18 routes", "POST    /auth/login"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("routes: missing %q in\n%s", want, stdout.String())
		}
	}
	stdout.Reset()
	if err := run([]string{"check"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "ok: 12 modules, 20 providers, 6 controllers") {
		t.Fatalf("check: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if err := run([]string{"graph", "-format", "mermaid"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), `subgraph m7["users"]`) {
		t.Fatalf("graph: %v\n%s", err, stdout.String())
	}

	// A wiring mistake is reported by check without starting the app.
	usersModule := filepath.Join("src", "modules", "users", "module.go")
	src, _ := os.ReadFile(usersModule)
	write(t, usersModule, strings.Replace(string(src), "dygo.Provide(NewService)", "", 1))
	stdout.Reset()
	stderr.Reset()
	err = run([]string{"check"}, &stdout, &stderr)
	if code, ok := err.(exitError); !ok || code != 1 || !strings.Contains(stderr.String(), "*users.Service") {
		t.Fatalf("broken check: %v\n%s", err, stderr.String())
	}
}

// TestNewModularProject checks more generators in a new project: names with
// a dash, a service added to an existing module, and dygo g auth with a name.
func TestNewModularProject(t *testing.T) {
	dygotest.Integration(t) // builds real programs
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "shop")
	var out bytes.Buffer
	if err := run([]string{"new", dir, "-replace", repo}, &out, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	t.Chdir(dir)
	for _, args := range [][]string{{"g", "res", "order-items"}, {"g", "mo", "auth"}, {"g", "s", "auth"}, {"g", "auth", "account"}, {"g", "command", "seed"}, {"g", "entity", "Cart", "-fields", "total:float buyer:email"}, {"g", "crud", "Cart"}} {
		if err := run(args, &out, &out); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
	}
	for _, want := range []string{
		"UPDATE app.module.go (Imports += orderitems.Module)",
		"UPDATE app.module.go (Imports += auth.Module)",
		"UPDATE src/modules/auth/module.go (Providers += dygo.Provide(NewService))",
		"UPDATE app.module.go (Imports += account.Module)",
		"UPDATE app.module.go (Providers += dygo.Provide(seed.NewCommand))",
		"UPDATE .env.example (AUTH_SECRET, AUTH_TOKEN_TTL)",
		"UPDATE app.module.go (Imports += carts.Module)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	for _, args := range [][]string{{"vet", "./..."}, {"test", "./..."}} {
		if b, err := exec.Command("go", args...).CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, b)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"routes"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "POST    /order-items") {
		t.Fatalf("routes: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if err := run([]string{"check"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "ok: 12 modules, 15 providers, 5 controllers") {
		t.Fatalf("check: %v\n%s%s", err, stdout.String(), stderr.String())
	}
}

// TestNewSecurityProject checks dygo new -security, with built-in tokens and
// with -oidc: the project vets and passes its tests, and its routes are
// locked by default.
func TestNewSecurityProject(t *testing.T) {
	dygotest.Integration(t) // builds real programs
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		oidc bool
	}{{"tokens", false}, {"oidc", true}} {
		name := tc.name
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "store")
			var out bytes.Buffer
			args := []string{"new", dir, "-security", "-replace", repo}
			if tc.oidc {
				args = append(args, "-oidc")
			}
			if err := run(args, &out, &out); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			t.Chdir(dir)
			if cfg := readFile(t, "dygo.json"); !strings.Contains(cfg, `"security": true`) {
				t.Fatalf("dygo.json:\n%s", cfg)
			}
			wantEnv, wantRoute := "AUTH_SECRET=", "POST    /auth/login"
			if tc.oidc {
				wantEnv, wantRoute = "OAUTH_ISSUER_URL=", "GET     /auth/me"
			}
			if env := readFile(t, ".env"); !strings.Contains(env, wantEnv) || !strings.Contains(env, "CORS_ALLOWED_ORIGINS=") {
				t.Fatalf(".env:\n%s", env)
			}
			for _, args := range [][]string{
				{"g", "res", "products", "-fields", "title:string price:float"},
				{"g", "res", "posts", "-public-read"},
				{"g", "entity", "Invoice", "-fields", "number:string"},
				{"g", "crud", "Invoice", "-write-role", "accountant"},
				{"g", "co", "billing"},
				{"g", "co", "hooks", "-public"},
			} {
				if err := run(args, &out, &out); err != nil {
					t.Fatalf("%v: %v\n%s", args, err, out.String())
				}
			}
			for _, args := range [][]string{{"vet", "./..."}, {"test", "./..."}} {
				if b, err := exec.Command("go", args...).CombinedOutput(); err != nil {
					t.Fatalf("go %v: %v\n%s", args, err, b)
				}
			}
			var stdout, stderr bytes.Buffer
			if err := run([]string{"routes"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), wantRoute) ||
				tc.oidc && strings.Contains(stdout.String(), "/auth/login") {
				t.Fatalf("routes: %v\n%s%s", err, stdout.String(), stderr.String())
			}
			if err := run([]string{"g", "auth"}, &out, &out); err == nil {
				t.Fatal("g auth in a security project must fail")
			}
		})
	}
}
