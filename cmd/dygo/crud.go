package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// entityPkg is an entity made by dygo g entity: a struct X with CreateX and
// UpdateX bodies in the same package.
type entityPkg struct {
	Dir, File, Entity string
}

// listEntities finds the entities under base.
func listEntities(base string) ([]entityPkg, error) {
	var out []entityPkg
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != base && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil // a broken file is not an entity
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.TYPE {
				continue
			}
			for _, s := range g.Specs {
				name := s.(*ast.TypeSpec).Name.Name
				if e, ok := strings.CutPrefix(name, "Create"); ok && e != "" && structType(f, e) != nil && structType(f, "Update"+e) != nil {
					out = append(out, entityPkg{Dir: filepath.Dir(path), File: path, Entity: e})
				}
			}
		}
		return nil
	})
	return out, err
}

// pickEntity finds the entity a name means: its type (Product), its package
// (products) or its directory.
func pickEntity(entities []entityPkg, base, name string) (entityPkg, bool) {
	pkg := ""
	if n, err := newNames(filepath.Base(name)); err == nil {
		pkg = n.Pkg
	}
	for _, e := range entities {
		if strings.EqualFold(e.Entity, name) || strings.EqualFold(e.Entity, pascal(splitWords(name))) {
			return e, true
		}
	}
	for _, e := range entities {
		if filepath.Base(e.Dir) == pkg || e.Dir == filepath.Join(base, filepath.FromSlash(name)) {
			return e, true
		}
	}
	return entityPkg{}, false
}

// generateCRUD reads an existing entity and writes the
// module, service, controller and test around it.
func generateCRUD(cwd string, proj project, base, name string, o genOptions, stdout io.Writer) error {
	entities, err := listEntities(base)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entities {
		names = append(names, e.Entity)
	}
	if name == "" {
		if o.Prompt == nil {
			return fmt.Errorf("name the entity: dygo g crud <Entity> (entities here: %s)", strings.Join(names, ", "))
		}
		if len(entities) == 0 {
			return fmt.Errorf("no entities in %s yet: create one with dygo g entity <name>", rel(cwd, base))
		}
		fmt.Fprintf(stdout, "\n Entities: %s\n The name of the entity to create CRUD for (e.g. %s):\n > ", strings.Join(names, ", "), names[0])
		sc := bufio.NewScanner(o.Prompt)
		if sc.Scan() {
			name = strings.TrimSpace(sc.Text())
		}
		if name == "" {
			return fmt.Errorf("no entity given")
		}
	}
	e, ok := pickEntity(entities, base, name)
	if !ok {
		msg := "no entity " + name + " found"
		if len(names) > 0 {
			msg += " (entities: " + strings.Join(names, ", ") + ")"
		}
		return fmt.Errorf("%s: create it first with dygo g entity %s", msg, name)
	}
	n, err := newNames(filepath.Base(e.Dir))
	if err != nil {
		return err
	}
	if sites, err := findModules(e.Dir); err != nil {
		return err
	} else if len(sites) > 0 {
		return fmt.Errorf("%s already has a module (%s): CRUD for %s may already exist", rel(cwd, e.Dir), rel(cwd, sites[0].File), e.Entity)
	}
	src, err := os.ReadFile(e.File)
	if err != nil {
		return err
	}
	for _, m := range []string{"Create" + e.Entity + ") Entity(", "Update" + e.Entity + ") Apply("} {
		if !strings.Contains(string(src), m) {
			return fmt.Errorf("%s has no %s...) method, which the generated service calls; add it (see dygo g entity)", rel(cwd, e.File), strings.TrimSuffix(m, "("))
		}
	}
	if out, ok := addIDTag(src, e.Entity); ok && !o.DryRun {
		if err := os.WriteFile(e.File, out, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "UPDATE %s (ID db:\"id,pk,auto\": the primary key)\n", rel(cwd, e.File))
	}
	fields, err := existingFields(e.File, e.Entity)
	if err != nil {
		return err
	}
	n.Entity = e.Entity
	n.Var = string(unicode.ToLower(rune(e.Entity[0]))) + e.Entity[1:]
	data := tmplData{names: n, Framework: frameworkPath, Fields: fields}
	if data.Sec, err = findSecurity(proj, o); err != nil {
		return err
	}
	at := func(path, tmpl string) file { return file{filepath.Join(e.Dir, path), "gen/" + tmpl} }
	files := []file{
		at("module.go", "resource_module.go.tmpl"),
		at("service.go", "resource_service.go.tmpl"),
		at("controller.go", "resource_controller.go.tmpl"),
		at("controller_test.go", "resource_controller_test.go.tmpl"),
	}
	reg := moduleEdit{Field: "Imports", Expr: n.Pkg + ".Module", ImportPath: proj.importPath(e.Dir)}
	regFrom := filepath.Dir(e.Dir)
	return writeAndRegister(cwd, proj, "crud", n, e.Dir, regFrom, files, data, reg, o.DryRun, stdout)
}

var goToFieldType = map[string]string{
	"string": "string", "int": "int", "int64": "int64", "float64": "float", "bool": "bool", "time.Time": "time",
}

// existingFields reads the create body's fields back as entity fields, so
// the generated test can fill them in. Email fields are the ones Validate
// checks with isEmail.
func existingFields(file, entity string) (fieldSet, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out fieldSet
	for _, fl := range structType(f, "Create"+entity).Fields.List {
		ft, ok := goToFieldType[string(src[fset.Position(fl.Type.Pos()).Offset:fset.Position(fl.Type.End()).Offset])]
		if !ok {
			continue // not a type the generator knows; the test leaves it zero
		}
		tag := ""
		if fl.Tag != nil {
			tag = reflect.StructTag(strings.Trim(fl.Tag.Value, "`")).Get("json")
		}
		jsonName, opts, _ := strings.Cut(tag, ",")
		for _, nm := range fl.Names {
			if ft == "string" && regexp.MustCompile(`isEmail\(\*?\w+\.`+nm.Name+`\)`).Match(src) {
				ft = "email"
			}
			js := jsonName
			if js == "" {
				js = nm.Name
			}
			out = append(out, entityField{Name: nm.Name, JSON: js, Type: ft, Optional: slices.Contains(strings.Split(opts, ","), "omitempty")})
		}
	}
	return out, nil
}

// ensureDatabase makes sure the app provides the *sql.DB the generated
// service needs. dygo new imports sql.Module(nil) in the root module; an app
// made before that gets it here. It also says when go.mod lacks the sql
// module.
func ensureDatabase(cwd string, proj project, stdout io.Writer) error {
	root := filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.RootModuleDir, cmp(proj.Config.Main, "."))))
	has := false
	for _, dir := range []string{root, filepath.Join(proj.Dir, "src", "database"), filepath.Join(proj.Dir, "internal", "database")} {
		for _, m := range []string{"sql.Module(", "postgres.Module(", "mysql.Module(", "sql.MemoryModule("} {
			has = has || packageContains(dir, m)
		}
	}
	if !has {
		sites, err := findModules(root)
		if err != nil {
			return err
		}
		site, ok := pickModule(sites)
		if !ok {
			fmt.Fprintf(stdout, "\nThe app has no database module: import sql.Module(nil) (%q) in the root module.\n", frameworkPath+"/sql")
			return nil
		}
		changed, err := editModule(site, moduleEdit{Field: "Imports", Expr: "sql.Module(nil)", ImportPath: frameworkPath + "/sql"})
		if err != nil {
			return err
		}
		if changed {
			fmt.Fprintf(stdout, "UPDATE %s (Imports += sql.Module(nil): the database in DATABASE_URL)\n", rel(cwd, site.File))
		}
	}
	if gomod, err := os.ReadFile(filepath.Join(proj.Dir, "go.mod")); err == nil && !strings.Contains(string(gomod), frameworkPath+"/sql ") {
		if err := addSQLModule(proj); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "\nRun go mod tidy to add %s/sql to go.mod.\n", frameworkPath)
	}
	return nil
}

// reIDField is the ID field of an entity and its tag.
var reIDField = regexp.MustCompile("(?m)^(\\s*ID\\s+u?int\\w*\\s+`)([^`]*)`")

// addIDTag gives the entity's ID field the db tag the repository needs to
// find the primary key, for entities made before dygo g entity wrote it.
func addIDTag(src []byte, entity string) ([]byte, bool) {
	start := strings.Index(string(src), "type "+entity+" struct {")
	if start < 0 {
		return nil, false
	}
	end := strings.Index(string(src[start:]), "\n}")
	if end < 0 {
		return nil, false
	}
	body := src[start : start+end]
	m := reIDField.FindSubmatchIndex(body)
	if m == nil || strings.Contains(string(body[m[4]:m[5]]), `db:"`) {
		return nil, false
	}
	at := start + m[5]
	out := slices.Concat(src[:at], []byte(` db:"id,pk,auto"`), src[at:])
	if f, err := format.Source(out); err == nil {
		out = f
	}
	return out, true
}
