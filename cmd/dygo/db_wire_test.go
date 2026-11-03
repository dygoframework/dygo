package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const memoryApp = `package main

import (
	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/sql"
)

var AppModule = &dygo.Module{
	Name:    "app",
	Imports: []*dygo.Module{sql.Module(nil)},
}
`

const plainApp = `package main

import "github.com/dygoframework/dygo"

var AppModule = &dygo.Module{Name: "app"}
`

func TestWireDriverModule(t *testing.T) {
	tests := []struct {
		name   string
		src    string // app.go, empty for none
		driver string
		want   []string // in app.go
		out    string   // in the output
	}{
		{"memory module replaced", memoryApp, "postgres",
			[]string{"postgres.Module()", `"github.com/dygoframework/dygo/sql/postgres"`}, "sql.Module(nil) -> postgres.Module()"},
		{"driver added to imports", plainApp, "mysql",
			[]string{"Imports: []*dygo.Module{mysql.Module()}", `"github.com/dygoframework/dygo/sql/mysql"`}, "Imports += mysql.Module()"},
		{"driver already there", strings.Replace(memoryApp, "sql.Module(nil)", "postgres.Module()", 1), "mysql",
			[]string{"postgres.Module()"}, ""},
		{"no root module", "package main\n\nfunc main() {}\n", "postgres", nil, "No root module found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			app := filepath.Join(dir, "app.go")
			write(t, app, tt.src)
			var out bytes.Buffer
			if err := wireDriverModule(dir, project{Dir: dir}, tt.driver, &out); err != nil {
				t.Fatal(err)
			}
			got := readFile(t, app)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("app.go lacks %q:\n%s", w, got)
				}
			}
			if !strings.Contains(out.String(), tt.out) {
				t.Errorf("output lacks %q: %s", tt.out, out.String())
			}
		})
	}
}

func TestReplaceMemoryModuleKeepsSQLImport(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "app.go")
	// sql. is still used elsewhere, so the sql import stays.
	write(t, app, strings.Replace(memoryApp, "\n}\n", "\n}\n\nvar _ = sql.Module\n", 1))
	var out bytes.Buffer
	done, err := replaceMemoryModule(dir, dir, "postgres", &out)
	if err != nil || !done {
		t.Fatalf("done %v, err %v", done, err)
	}
	if got := readFile(t, app); !strings.Contains(got, `"github.com/dygoframework/dygo/sql"`) {
		t.Fatalf("sql import dropped:\n%s", got)
	}
	// A file that no longer parses after the edit is an error.
	write(t, app, strings.Replace(memoryApp, "\n}\n", "\n", 1))
	if _, err := replaceMemoryModule(dir, dir, "postgres", &out); err == nil {
		t.Fatal("no error for a broken file")
	}
	if err := os.Remove(app); err != nil {
		t.Fatal(err)
	}
	if done, err := replaceMemoryModule(dir, dir, "postgres", &out); done || err != nil {
		t.Fatalf("empty dir: done %v, err %v", done, err)
	}
}
