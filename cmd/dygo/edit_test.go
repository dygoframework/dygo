package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNames(t *testing.T) {
	for _, tc := range []struct{ in, kebab, pkg, entity, varName string }{
		{"users", "users", "users", "User", "user"},
		{"order-items", "order-items", "orderitems", "OrderItem", "orderItem"},
		{"OrderItems", "order-items", "orderitems", "OrderItem", "orderItem"},
		{"categories", "categories", "categories", "Category", "category"},
		{"addresses", "addresses", "addresses", "Address", "address"},
		{"status", "status", "status", "StatusItem", "statusItem"},
		{"api_keys", "api-keys", "apikeys", "APIKey", "apiKey"},
	} {
		n, err := newNames(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if n.Kebab != tc.kebab || n.Pkg != tc.pkg || n.Entity != tc.entity || n.Var != tc.varName {
			t.Errorf("%s: %+v", tc.in, n)
		}
	}
	for _, bad := range []string{"", "9lives", "a/b", "x!"} {
		if _, err := newNames(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestEntityNames(t *testing.T) {
	for _, tc := range []struct{ in, pkg, entity string }{
		{"Product", "products", "Product"},
		{"Products", "products", "Product"},
		{"product", "products", "Product"},
		{"OrderItem", "orderitems", "OrderItem"},
		{"Category", "categories", "Category"},
		{"Address", "addresses", "Address"},
		{"Status", "statuses", "Status"},
		{"Box", "boxes", "Box"},
		{"Key", "keys", "Key"},
	} {
		n, err := entityNames(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if n.Pkg != tc.pkg || n.Entity != tc.entity {
			t.Errorf("%s: package %s, entity %s; want %s, %s", tc.in, n.Pkg, n.Entity, tc.pkg, tc.entity)
		}
	}
}

func TestModuleEdit(t *testing.T) {
	const header = "package app\n\nimport \"github.com/dygoframework/dygo\"\n\n"
	for _, tc := range []struct {
		name, src string
		edit      moduleEdit
		want      string // a substring of the result; "" means unchanged
	}{
		{
			name: "missing field",
			src:  header + "var Module = &dygo.Module{\n\tName: \"app\",\n}\n",
			edit: moduleEdit{Field: "Providers", Expr: "dygo.Provide(NewService)"},
			want: "\tName:      \"app\",\n\tProviders: []dygo.Provider{dygo.Provide(NewService)},\n}",
		},
		{
			name: "missing field, one line literal",
			src:  header + "var Module = &dygo.Module{Name: \"app\"}\n",
			edit: moduleEdit{Field: "Controllers", Expr: "dygo.Provide(NewController)"},
			want: "&dygo.Module{Name: \"app\", Controllers: []dygo.Provider{dygo.Provide(NewController)}}",
		},
		{
			name: "one line list",
			src:  header + "var Module = &dygo.Module{\n\tName:      \"app\",\n\tProviders: []dygo.Provider{dygo.Provide(NewA)},\n}\n",
			edit: moduleEdit{Field: "Providers", Expr: "dygo.Provide(NewB)"},
			want: "[]dygo.Provider{dygo.Provide(NewA), dygo.Provide(NewB)}",
		},
		{
			name: "multi line list keeps comments, adds import",
			src: header + "var Module = &dygo.Module{\n\tName: \"app\",\n\tImports: []*dygo.Module{\n" +
				"\t\t// logging first\n\t\tlogMod,\n\t},\n}\n",
			edit: moduleEdit{Field: "Imports", Expr: "users.Module", ImportPath: "example.com/app/users"},
			want: "\t\t// logging first\n\t\tlogMod,\n\t\tusers.Module,\n\t},",
		},
		{
			name: "empty list",
			src:  header + "var Module = &dygo.Module{Name: \"app\", Imports: []*dygo.Module{}}\n",
			edit: moduleEdit{Field: "Imports", Expr: "users.Module", ImportPath: "example.com/app/users"},
			want: "Imports: []*dygo.Module{users.Module}",
		},
		{
			name: "already registered",
			src:  header + "var Module = &dygo.Module{Name: \"app\", Imports: []*dygo.Module{users.Module}}\n",
			edit: moduleEdit{Field: "Imports", Expr: "users.Module", ImportPath: "example.com/app/users"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "module.go")
			if err := os.WriteFile(file, []byte(tc.src), 0o644); err != nil {
				t.Fatal(err)
			}
			sites, err := findModules(filepath.Dir(file))
			if err != nil || len(sites) != 1 {
				t.Fatalf("sites %v %v", sites, err)
			}
			changed, err := editModule(sites[0], tc.edit)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(file)
			if tc.want == "" {
				if changed || string(got) != tc.src {
					t.Fatalf("changed:\n%s", got)
				}
				return
			}
			if !changed || !strings.Contains(string(got), tc.want) {
				t.Fatalf("got:\n%s\nwant substring:\n%s", got, tc.want)
			}
			if tc.edit.ImportPath != "" && !strings.Contains(string(got), `"`+tc.edit.ImportPath+`"`) {
				t.Fatalf("import missing:\n%s", got)
			}
		})
	}
}

func TestAddImport(t *testing.T) {
	for _, src := range []string{
		"package x\n\nvar v = 1\n",
		"package x\n\nimport \"fmt\"\n\nvar v = fmt.Sprint()\n",
		"package x\n\nimport (\n\t\"fmt\"\n)\n\nvar v = fmt.Sprint()\n",
	} {
		out, err := addImport("x.go", []byte(src), "example.com/y")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"example.com/y"`) || (strings.Contains(src, "fmt") && !strings.Contains(string(out), `"fmt"`)) {
			t.Fatalf("got:\n%s", out)
		}
	}
}

func TestParseFlags(t *testing.T) {
	fs := newFlags("x")
	format := fs.String("format", "text", "")
	pos, rest, err := parseFlags(fs, []string{"./cmd/api", "-format", "dot", "--", "-port", "1"})
	if err != nil || *format != "dot" || len(pos) != 1 || pos[0] != "./cmd/api" || strings.Join(rest, " ") != "-port 1" {
		t.Fatalf("%v %q %v %v", err, *format, pos, rest)
	}
}

func TestWatched(t *testing.T) {
	for name, want := range map[string]bool{
		"main.go": true, "main_test.go": false, "go.mod": true, ".env": true, ".env.local": true,
		".env.example": false, "README.md": false,
	} {
		if watched(name) != want {
			t.Errorf("%s: want %v", name, want)
		}
	}
}
