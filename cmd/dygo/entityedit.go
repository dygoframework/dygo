package main

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// findEntity returns the non-test file in dir that declares the entity
// struct, or "".
func findEntity(dir, entity string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return "", err
		}
		if structType(f, entity) != nil {
			return path, nil
		}
	}
	return "", nil
}

// entityFieldNames returns the Go names of the entity's fields except ID.
func entityFieldNames(file, entity string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, fl := range structType(f, entity).Fields.List {
		for _, n := range fl.Names {
			if n.Name != "ID" {
				names = append(names, n.Name)
			}
		}
	}
	return names, nil
}

func structType(f *ast.File, name string) *ast.StructType {
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.TYPE {
			continue
		}
		for _, s := range g.Specs {
			if ts := s.(*ast.TypeSpec); ts.Name.Name == name {
				st, _ := ts.Type.(*ast.StructType)
				return st
			}
		}
	}
	return nil
}

// insertion is text to insert at a byte offset.
type insertion struct {
	at   int
	text string
}

// applyInsertions inserts from the end, so earlier offsets stay valid.
// Insertions at the same offset keep their order.
func applyInsertions(src []byte, ins []insertion) []byte {
	slices.SortStableFunc(ins, func(a, b insertion) int { return a.at - b.at })
	out := slices.Clone(src)
	for i := len(ins) - 1; i >= 0; i-- {
		out = slices.Concat(out[:ins[i].at], []byte(ins[i].text), out[ins[i].at:])
	}
	return out
}

// litInsertion adds an element to a composite literal.
func litInsertion(fset *token.FileSet, lit *ast.CompositeLit, elt string) insertion {
	at := fset.Position(lit.Rbrace).Offset
	if n := len(lit.Elts); n > 0 && fset.Position(lit.Elts[n-1].End()).Line == fset.Position(lit.Rbrace).Line {
		return insertion{at, ", " + strings.TrimSuffix(elt, ",")}
	}
	return insertion{at, elt + "\n"}
}

// addFields adds fields to an entity made by dygo g entity or resource: the
// entity, the create and update bodies, their Validate, Entity and Apply
// methods, and the create bodies in the package's tests. Everything else in
// the files stays as it is.
func addFields(cwd, dir, file, entity string, fields []entityField, dryRun bool, stdout io.Writer) error {
	src, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	have := map[string]bool{}
	for _, fl := range structType(f, entity).Fields.List {
		for _, n := range fl.Names {
			have[n.Name] = true
		}
	}
	var add fieldSet
	for _, fd := range fields {
		if have[fd.Name] {
			fmt.Fprintf(stdout, "SKIP %s.%s (already a field)\n", entity, fd.Name)
			continue
		}
		add = append(add, fd)
	}
	if len(add) == 0 {
		return nil
	}
	var ins []insertion
	var missing []string
	each := func(fn func(entityField) string) string {
		var b strings.Builder
		for _, fd := range add {
			if s := fn(fd); s != "" {
				b.WriteString(s + "\n")
			}
		}
		return b.String()
	}
	for _, s := range []struct {
		name string
		line func(entityField) string
	}{
		{entity, entityField.EntityField},
		{"Create" + entity, entityField.CreateField},
		{"Update" + entity, entityField.UpdateField},
	} {
		if st := structType(f, s.name); st != nil {
			ins = append(ins, insertion{off(st.Fields.Closing), each(s.line)})
		} else if s.name != entity {
			missing = append(missing, "type "+s.name)
		}
	}
	found := map[string]bool{}
	hasIsEmail := false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if fn.Recv == nil {
			hasIsEmail = hasIsEmail || fn.Name.Name == "isEmail"
			continue
		}
		recvType, recv := receiver(fn)
		key := recvType + "." + fn.Name.Name
		last := len(fn.Body.List) - 1
		switch key {
		case "Create" + entity + ".Validate", "Update" + entity + ".Validate":
			if last < 0 {
				continue
			}
			ret, ok := fn.Body.List[last].(*ast.ReturnStmt)
			if !ok {
				continue
			}
			check := entityField.CreateCheck
			if strings.HasPrefix(key, "Update") {
				check = entityField.UpdateCheck
			}
			ins = append(ins, insertion{off(ret.Pos()), each(func(fd entityField) string { return check(fd, recv) })})
		case "Create" + entity + ".Entity":
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if lit, ok := n.(*ast.CompositeLit); ok && isIdent(lit.Type, entity) {
					for _, fd := range add {
						ins = append(ins, litInsertion(fset, lit, fd.EntityElt(recv)))
					}
					return false
				}
				return true
			})
		case "Update" + entity + ".Apply":
			param := "e"
			if ps := fn.Type.Params.List; len(ps) == 1 && len(ps[0].Names) == 1 {
				param = ps[0].Names[0].Name
			}
			ins = append(ins, insertion{off(fn.Body.Rbrace), each(func(fd entityField) string { return fd.ApplyLine(recv, param) })})
		default:
			continue
		}
		found[key] = true
	}
	for _, k := range []string{"Create" + entity + ".Validate", "Create" + entity + ".Entity", "Update" + entity + ".Validate", "Update" + entity + ".Apply"} {
		if !found[k] {
			missing = append(missing, "func ("+strings.Replace(k, ".", ") ", 1))
		}
	}
	// isEmail may live in another file of the package (flat files share one).
	elsewhere := !hasIsEmail && packageContains(dir, "func isEmail(")
	if add.HasEmail() && !hasIsEmail && !elsewhere {
		ins = append(ins, insertion{len(src), "\nfunc isEmail(s string) bool {\na, err := mail.ParseAddress(s)\nreturn err == nil && a.Address == s\n}\n"})
	}
	out := applyInsertions(src, ins)
	for _, imp := range add.Imports() {
		if imp == "net/mail" && elsewhere {
			continue
		}
		if out, err = addImport(file, out, imp); err != nil {
			return fmt.Errorf("%s: edited source does not parse: %w", file, err)
		}
	}
	if out, err = format.Source(out); err != nil {
		return fmt.Errorf("%s: edited source does not parse: %w", file, err)
	}
	names := make([]string, len(add))
	for i, fd := range add {
		names[i] = fd.JSON
	}
	fmt.Fprintf(stdout, "UPDATE %s (+ %s)\n", rel(cwd, file), strings.Join(names, ", "))
	if !dryRun {
		if err := os.WriteFile(file, out, 0o644); err != nil {
			return err
		}
	}
	if err := addSampleFields(cwd, dir, entity, add, dryRun, stdout); err != nil {
		return err
	}
	if len(missing) > 0 {
		fmt.Fprintf(stdout, "\nNot found, so not updated: %s. Add the new fields there yourself.\n", strings.Join(missing, ", "))
	}
	fmt.Fprintf(stdout, "\nSuccess! Next: go test ./%s, and dygo g crud %s if it has no CRUD yet.\n", filepath.ToSlash(rel(cwd, dir)), entity)
	return nil
}

// addSampleFields adds sample values to the non-empty Create<Entity>{...}
// literals in the package's tests, so they still pass validation.
func addSampleFields(cwd, dir, entity string, add fieldSet, dryRun bool, stdout io.Writer) error {
	tests, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
	for _, file := range tests {
		src, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		var ins []insertion
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.CompositeLit); ok && isIdent(lit.Type, "Create"+entity) && len(lit.Elts) > 0 {
				for _, fd := range add {
					ins = append(ins, litInsertion(fset, lit, fd.SampleElt()))
				}
			}
			return true
		})
		if len(ins) == 0 {
			continue
		}
		out := applyInsertions(src, ins)
		for _, imp := range add.TestImports() {
			if out, err = addImport(file, out, imp); err != nil {
				return err
			}
		}
		if out, err = format.Source(out); err != nil {
			return fmt.Errorf("%s: edited source does not parse: %w", file, err)
		}
		fmt.Fprintf(stdout, "UPDATE %s (sample values)\n", rel(cwd, file))
		if !dryRun {
			if err := os.WriteFile(file, out, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// receiver returns the receiver's type name, without a *, and its name.
func receiver(fn *ast.FuncDecl) (typ, name string) {
	r := fn.Recv.List[0]
	t := r.Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		typ = id.Name
	}
	if len(r.Names) > 0 {
		name = r.Names[0].Name
	}
	return typ, name
}

// testRoute is a route in the generated routes test.
type testRoute struct {
	Method, Path, SamplePath, Todo string
	Status                         int
	Ready, Body                    bool
}

var pathParamRE = regexp.MustCompile(`\{[^}]*\}`)

// generateTest writes routes_test.go for the package in dir from the routes
// the running app registers for it.
func generateTest(cwd string, proj project, dir string, data tmplData, dryRun bool, stdout io.Writer) error {
	if !packageContains(dir, "var "+data.P()+"Module") {
		return fmt.Errorf("%s has no var %sModule: dygo g test works on a feature module", rel(cwd, dir), data.P())
	}
	main := filepath.Join(proj.Dir, filepath.FromSlash(cmp(proj.Config.Main, ".")))
	pkg := filepath.ToSlash(rel(cwd, main))
	if !strings.HasPrefix(pkg, ".") {
		pkg = "./" + pkg
	}
	rep, err := inspect(pkg, true)
	if err != nil {
		return err
	}
	if len(rep.Errors) > 0 {
		return printErrors(stdout, rep)
	}
	prefix := data.Pkg + "." // handlers are named like products.(*Controller).List
	if data.FlatPkg != "" {
		prefix = data.FlatPkg + ".(*" + data.P() + "Controller)." // main.(*ProductsController).List
	}
	for _, r := range rep.Routes {
		if !strings.HasPrefix(r.Handler, prefix) || r.Method == "" {
			continue
		}
		path := strings.TrimSuffix(r.Path, "{$}")
		tr := testRoute{Method: r.Method, Path: path, SamplePath: pathParamRE.ReplaceAllString(path, "1"), Status: r.Status}
		if tr.Status == 0 {
			switch {
			case strings.HasSuffix(r.Output, "NoContent"):
				tr.Status = 204
			case r.Method == "POST":
				tr.Status = 201
			default:
				tr.Status = 200
			}
		}
		tr.Body = r.Input != "" && r.Input != "struct {}" && (r.Method == "POST" || r.Method == "PUT" || r.Method == "PATCH")
		tr.Ready = r.Method == "GET" && !strings.Contains(path, "{")
		switch {
		case tr.Body:
			tr.Todo = "send a valid " + shortType(r.Input) + " body"
		default:
			tr.Todo = "create the data this route needs, and use real path parameters"
		}
		data.Routes = append(data.Routes, tr)
	}
	if len(data.Routes) == 0 {
		return fmt.Errorf("the app has no routes from %s: is its module imported into the root module?", proj.importPath(dir))
	}
	data.UsesSQL = packageContains(dir, "sql.ForFeature[")
	name := "routes_test.go"
	if data.FlatPkg != "" {
		name = flatName(data.names, name)
	}
	files := []file{{filepath.Join(dir, name), "gen/routes_test.go.tmpl"}}
	contents, err := render(files, data)
	if err != nil {
		return err
	}
	if err := writeNew(files, contents, cwd, stdout, dryRun); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\nNext: fill in the skipped routes, then go test ./%s\n", filepath.ToSlash(rel(proj.Dir, dir)))
	return nil
}
