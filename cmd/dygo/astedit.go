package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// frameworkPath is the import path of the dygo package.
const frameworkPath = "github.com/dygoframework/dygo"

// moduleSite is a &dygo.Module{...} literal in a source file.
type moduleSite struct {
	File string
	Name string // the literal's Name field, if it is a string literal
	Pkg  string // the file's package name
}

// errNoModule means no module literal was found in a directory or above it.
var errNoModule = errors.New("no &dygo.Module{...} literal found")

// findModules returns the module literals in the non-test files of dir.
func findModules(dir string) ([]moduleSite, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var sites []moduleSite
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file := filepath.Join(dir, name)
		src, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, lit := range moduleLits(f) {
			sites = append(sites, moduleSite{File: file, Name: litName(lit), Pkg: f.Name.Name})
		}
	}
	return sites, nil
}

// nearestModule finds the module literal in dir or the closest parent, up to
// stop. Among several literals in one directory it takes the one named like
// the package or "app".
func nearestModule(dir, stop string) (moduleSite, error) {
	for d := dir; ; d = filepath.Dir(d) {
		sites, err := findModules(d)
		if err != nil {
			return moduleSite{}, err
		}
		if s, ok := pickModule(sites); ok {
			return s, nil
		}
		if len(sites) > 1 {
			var names []string
			for _, s := range sites {
				names = append(names, fmt.Sprintf("%q in %s", s.Name, s.File))
			}
			return moduleSite{}, fmt.Errorf("several modules in %s (%s); name one %q to pick it", d, strings.Join(names, ", "), "app")
		}
		if d == stop || filepath.Dir(d) == d || !strings.HasPrefix(d, stop) {
			return moduleSite{}, errNoModule
		}
	}
}

func pickModule(sites []moduleSite) (moduleSite, bool) {
	if len(sites) == 1 {
		return sites[0], true
	}
	for _, s := range sites {
		if s.Name == "app" || s.Name == s.Pkg {
			return s, true
		}
	}
	return moduleSite{}, false
}

// moduleLits finds &dygo.Module{...} in a file, with dygo under any import name.
func moduleLits(f *ast.File) []*ast.CompositeLit {
	pkg := importName(f, frameworkPath)
	if pkg == "" {
		return nil
	}
	var lits []*ast.CompositeLit
	ast.Inspect(f, func(n ast.Node) bool {
		u, ok := n.(*ast.UnaryExpr)
		if !ok || u.Op != token.AND {
			return true
		}
		lit, ok := u.X.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if sel, ok := lit.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Module" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
				lits = append(lits, lit)
			}
		}
		return true
	})
	return lits
}

// importName is the name f uses for the import path, or "".
func importName(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == path {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return path[strings.LastIndex(path, "/")+1:]
		}
	}
	return ""
}

func litName(lit *ast.CompositeLit) string {
	if kv := field(lit, "Name"); kv != nil {
		if b, ok := kv.Value.(*ast.BasicLit); ok {
			s, _ := strconv.Unquote(b.Value)
			return s
		}
	}
	return ""
}

func field(lit *ast.CompositeLit, name string) *ast.KeyValueExpr {
	for _, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name {
				return kv
			}
		}
	}
	return nil
}

// moduleEdit adds an element to a slice field of the module literal named
// site.Name in site.File, and the import the element needs. Edits are text
// insertions at AST positions followed by gofmt, so comments and layout stay.
type moduleEdit struct {
	Field      string // Imports, Providers or Controllers
	Expr       string // users.Module, dygo.Provide(NewService)
	ImportPath string // added to the file's imports unless empty
}

// apply returns the edited source, and false when the element was already there.
func (e moduleEdit) apply(site moduleSite, src []byte) ([]byte, bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, site.File, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, false, err
	}
	var lit *ast.CompositeLit
	for _, l := range moduleLits(f) {
		if litName(l) == site.Name {
			lit = l
			break
		}
	}
	if lit == nil {
		return nil, false, fmt.Errorf("%s: module %q not found", site.File, site.Name)
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	expr := e.Expr
	var at int
	var text string
	if kv := field(lit, e.Field); kv != nil {
		list, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			return nil, false, fmt.Errorf("%s: module %q: %s is not a slice literal; add %s yourself", site.File, site.Name, e.Field, expr)
		}
		for _, el := range list.Elts {
			if strings.Join(strings.Fields(string(src[off(el.Pos()):off(el.End())])), "") == strings.Join(strings.Fields(expr), "") {
				return src, false, nil
			}
		}
		at = off(list.Rbrace)
		switch {
		case len(list.Elts) == 0:
			text = expr
		case fset.Position(list.Elts[len(list.Elts)-1].End()).Line == fset.Position(list.Rbrace).Line:
			text = ", " + expr
		default:
			text = expr + ",\n"
		}
	} else {
		typ := map[string]string{"Imports": "[]*%s.Module", "Providers": "[]%s.Provider", "Controllers": "[]%s.Provider"}[e.Field]
		typ = fmt.Sprintf(typ, importName(f, frameworkPath))
		at = off(lit.Rbrace)
		text = fmt.Sprintf("%s: %s{%s},\n", e.Field, typ, expr)
		if len(lit.Elts) > 0 && fset.Position(lit.Elts[len(lit.Elts)-1].End()).Line == fset.Position(lit.Rbrace).Line {
			text = ", " + strings.TrimSuffix(text, ",\n")
		}
	}
	out := slices.Concat(src[:at], []byte(text), src[at:])
	if e.ImportPath != "" {
		if out, err = addImport(site.File, out, e.ImportPath); err != nil {
			return nil, false, fmt.Errorf("%s: edited source does not parse: %w", site.File, err)
		}
	}
	formatted, err := format.Source(out)
	if err != nil {
		return nil, false, fmt.Errorf("%s: edited source does not parse: %w", site.File, err)
	}
	return formatted, true, nil
}

// addImport adds path to the file's imports if it is missing.
func addImport(file string, src []byte, path string) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if importName(f, path) != "" {
		return src, nil
	}
	quoted := strconv.Quote(path)
	var out []byte
	switch {
	case len(f.Imports) == 0:
		at := fset.Position(f.Name.End()).Offset
		out = slices.Concat(src[:at], []byte("\n\nimport "+quoted+"\n"), src[at:])
	default:
		var decl *ast.GenDecl
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
				decl = g
				if g.Lparen.IsValid() {
					break
				}
			}
		}
		if decl.Lparen.IsValid() {
			// Join the group of imports from the same host (after its last
			// one), or start a new group, so project imports end up apart
			// from the framework's.
			text := "\n" + quoted + "\n"
			at := fset.Position(decl.Rparen).Offset
			for _, s := range decl.Specs {
				if p, _ := strconv.Unquote(s.(*ast.ImportSpec).Path.Value); firstElem(p) == firstElem(path) {
					// After the line, so a trailing comment stays with its import.
					end := fset.Position(s.End()).Offset
					if nl := bytes.IndexByte(src[end:], '\n'); nl >= 0 {
						end += nl
					}
					text, at = "\n"+quoted, end
				}
			}
			out = slices.Concat(src[:at], []byte(text), src[at:])
		} else {
			start, end := fset.Position(decl.Pos()).Offset, fset.Position(decl.End()).Offset
			spec := strings.TrimSpace(strings.TrimPrefix(string(src[start:end]), "import"))
			sep := "\n\n"
			if p, _ := strconv.Unquote(decl.Specs[0].(*ast.ImportSpec).Path.Value); firstElem(p) == firstElem(path) {
				sep = "\n"
			}
			out = slices.Concat(src[:start], []byte("import (\n"+spec+sep+quoted+"\n)"), src[end:])
		}
	}
	return format.Source(out)
}

// editModule applies edits to the module literal at site and writes the file.
// It reports whether anything changed.
func editModule(site moduleSite, edits ...moduleEdit) (bool, error) {
	src, err := os.ReadFile(site.File)
	if err != nil {
		return false, err
	}
	orig := src
	for _, e := range edits {
		if src, _, err = e.apply(site, src); err != nil {
			return false, err
		}
	}
	if bytes.Equal(src, orig) {
		return false, nil
	}
	return true, os.WriteFile(site.File, src, 0o644)
}

func firstElem(path string) string {
	first, _, _ := strings.Cut(path, "/")
	return first
}
