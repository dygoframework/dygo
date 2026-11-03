package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// smallProject writes go.mod and a root module into a temp dir and makes it
// the working directory.
func smallProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	write(t, filepath.Join(dir, "app.go"), rootModule)
	t.Chdir(dir)
	return dir
}

func TestRunErrors(t *testing.T) {
	dir := smallProject(t)
	write(t, filepath.Join(dir, "full", "x.txt"), "x")
	tests := []struct {
		args []string
		want string
	}{
		{nil, "usage"},
		{[]string{"nope"}, `unknown command "nope"`},
		{[]string{"new"}, "missing project name"},
		{[]string{"new", "a", "b"}, `unexpected argument "b"`},
		{[]string{"new", "shop", "-oidc"}, "-oidc goes with -security"},
		{[]string{"new", "1shop"}, "must start with a letter"},
		{[]string{"new", "full", "-skip-tidy"}, "exists and is not empty"},
		{[]string{"new", "shop", "-replace", "/no/such/dir"}, "-replace:"},
		{[]string{"new", "shop2", "-skip-tidy", "-db", "oracle"}, "-db: unknown database"},
		{[]string{"g"}, "missing kind and name"},
		{[]string{"g", "spaceship", "x"}, `unknown kind "spaceship"`},
		{[]string{"g", "service"}, "missing name"},
		{[]string{"g", "service", "a", "b"}, `unexpected argument "b"`},
		{[]string{"g", "service", "a", "-oidc"}, "-oidc is for dygo g security"},
		{[]string{"g", "service", "a", "-public"}, "-public, -public-read and -write-role are for"},
		{[]string{"g", "service", "a", "-safe"}, "-safe is for dygo g migration"},
		{[]string{"g", "service", "a", "-rename", "a:b"}, "-rename is for dygo g migration"},
		{[]string{"g", "entity", "a", "-fields", "x:spaceship"}, "spaceship"},
		{[]string{"g", "-nope"}, "flag provided but not defined"},
		{[]string{"info", "a", "b"}, `unexpected argument "b"`},
		{[]string{"check", "-nope"}, "flag provided but not defined"},
		{[]string{"routes", "a", "b"}, `unexpected argument "b"`},
		{[]string{"start", "-nope"}, "flag provided but not defined"},
		{[]string{"gen", "-nope"}, "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var out bytes.Buffer
			err := run(tt.args, &out, &out)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want an error with %q, got %v", tt.want, err)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"version"}, &out, &out); err != nil || !strings.HasPrefix(out.String(), "dygo ") {
		t.Fatalf("%v %q", err, out.String())
	}
	if exitError(3).Error() != "exit status 3" {
		t.Fatal(exitError(3).Error())
	}
}

// TestMainExitStatus runs main in a child process: the test binary itself,
// with DYGO_TEST_MAIN set so TestHelperMain calls main.
func TestMainExitStatus(t *testing.T) {
	tests := []struct {
		args   []string
		status int
		stderr string
	}{
		{nil, 2, "Usage: dygo <command>"},
		{[]string{"nope"}, 1, `dygo: unknown command "nope"`},
		{[]string{"g"}, 1, "dygo generate: missing kind"},
		{[]string{"version"}, 0, ""},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestHelperMain$", "--"}, tt.args...)...)
			cmd.Env = append(os.Environ(), "DYGO_TEST_MAIN=1")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			status := 0
			if ee, ok := err.(*exec.ExitError); ok {
				status = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if status != tt.status || !strings.Contains(stderr.String(), tt.stderr) {
				t.Fatalf("status %d (want %d), stderr:\n%s", status, tt.status, stderr.String())
			}
		})
	}
}

func TestHelperMain(*testing.T) {
	if os.Getenv("DYGO_TEST_MAIN") == "" {
		return
	}
	for i, a := range os.Args {
		if a == "--" {
			os.Args = append([]string{"dygo"}, os.Args[i+1:]...)
			break
		}
	}
	main()
	os.Exit(0)
}

func TestCRUDErrors(t *testing.T) {
	dir := smallProject(t)
	var out bytes.Buffer
	gen := func(name string, o genOptions) error { return generate(dir, "crud", name, o, &out) }
	if err := gen("", genOptions{}); err == nil || !strings.Contains(err.Error(), "name the entity") {
		t.Fatalf("no name: %v", err)
	}
	if err := gen("", genOptions{Prompt: strings.NewReader("Invoice\n")}); err == nil || !strings.Contains(err.Error(), "no entities in") {
		t.Fatalf("no entities: %v", err)
	}
	fields, _ := parseFields("number:string")
	if err := generate(dir, "entity", "Invoice", genOptions{Fields: fields}, &out); err != nil {
		t.Fatal(err)
	}
	if err := gen("", genOptions{Prompt: strings.NewReader("\n")}); err == nil || !strings.Contains(err.Error(), "no entity given") {
		t.Fatalf("empty answer: %v", err)
	}
	if err := gen("Nope", genOptions{}); err == nil || !strings.Contains(err.Error(), "no entity Nope found (entities: Invoice)") {
		t.Fatalf("unknown entity: %v", err)
	}
	// Without the Apply method the generated service would not compile.
	entity := filepath.Join(dir, "src", "modules", "invoices", "invoices.go")
	if _, err := os.Stat(entity); err != nil {
		entity = filepath.Join(dir, "invoices", "invoices.go")
	}
	src := readFile(t, entity)
	write(t, entity, strings.Replace(src, ") Apply(", ") Merge(", 1))
	if err := gen("invoices", genOptions{}); err == nil || !strings.Contains(err.Error(), "Apply...) method") {
		t.Fatalf("no Apply: %v", err)
	}
	write(t, entity, src)
	out.Reset()
	if err := gen("", genOptions{Prompt: strings.NewReader("invoices\n")}); err != nil {
		t.Fatalf("by package name: %v\n%s", err, out.String())
	}
	if err := gen("Invoice", genOptions{}); err == nil || !strings.Contains(err.Error(), "already has a module") {
		t.Fatalf("twice: %v", err)
	}
}

func TestEntityAgain(t *testing.T) {
	dir := smallProject(t)
	var out bytes.Buffer
	fields, _ := parseFields("number:string")
	if err := generate(dir, "entity", "Invoice", genOptions{Fields: fields}, &out); err != nil {
		t.Fatal(err)
	}
	if err := generate(dir, "entity", "Invoice", genOptions{}, &out); err == nil || !strings.Contains(err.Error(), "pass -fields to add fields") {
		t.Fatalf("again without fields: %v", err)
	}
	out.Reset()
	if err := generate(dir, "entity", "Invoice", genOptions{Prompt: strings.NewReader("\n")}, &out); err != nil || !strings.Contains(out.String(), "No new fields.") {
		t.Fatalf("again, no answer: %v\n%s", err, out.String())
	}
}

func TestRegisterWithoutModule(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	write(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	var out bytes.Buffer
	if err := generate(dir, "service", "mailer", genOptions{}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No module found in") {
		t.Fatalf("output:\n%s", out.String())
	}
}
