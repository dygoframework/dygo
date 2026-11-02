package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// names are the spellings of a feature name such as "order-items".
type names struct {
	Kebab  string // order-items: URL paths and module names
	Pkg    string // orderitems: the Go package
	Pascal string // OrderItems
	Entity string // OrderItem: the resource's entity type
	Var    string // orderItem: a local variable for one entity
}

func newNames(name string) (names, error) {
	words := splitWords(name)
	if len(words) == 0 {
		return names{}, fmt.Errorf("invalid name %q", name)
	}
	if !unicode.IsLetter(rune(words[0][0])) {
		return names{}, fmt.Errorf("invalid name %q: must start with a letter", name)
	}
	n := names{Kebab: strings.Join(words, "-"), Pkg: strings.Join(words, "")}
	single := append([]string(nil), words...)
	single[len(single)-1] = singular(single[len(single)-1])
	n.Pascal = pascal(words)
	n.Entity = pascal(single)
	n.Var = single[0] + pascal(single[1:])
	if n.Entity == n.Pascal {
		// "status": the entity would be named like the package.
		n.Entity += "Item"
		n.Var += "Item"
	}
	return n, nil
}

// splitWords splits on separators and lower to upper case changes, and
// lowercases: "OrderItems", "order_items" and "order-items" give [order items].
func splitWords(s string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '-' || r == '_' || r == ' ' || r == '.':
			flush()
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			return nil
		case unicode.IsUpper(r) && i > 0 && unicode.IsLower(rs[i-1]):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

func pascal(words []string) string {
	var b strings.Builder
	for _, w := range words {
		if w == "id" || w == "api" || w == "url" || w == "http" {
			b.WriteString(strings.ToUpper(w))
			continue
		}
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

// singular handles regular English plurals, which covers most resource names.
func singular(w string) string {
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 3:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "sses"), strings.HasSuffix(w, "uses"), strings.HasSuffix(w, "xes"), strings.HasSuffix(w, "ches"), strings.HasSuffix(w, "shes"):
		return w[:len(w)-2]
	case strings.HasSuffix(w, "ss"), strings.HasSuffix(w, "us"):
		return w
	case strings.HasSuffix(w, "s") && len(w) > 1:
		return w[:len(w)-1]
	}
	return w
}

// plural is the reverse of singular for regular English nouns.
func plural(w string) string {
	switch {
	case strings.HasSuffix(w, "y") && len(w) > 1 && !strings.ContainsRune("aeiou", rune(w[len(w)-2])):
		return w[:len(w)-1] + "ies"
	case strings.HasSuffix(w, "s"), strings.HasSuffix(w, "x"), strings.HasSuffix(w, "ch"), strings.HasSuffix(w, "sh"):
		return w + "es"
	}
	return w + "s"
}

// entityNames names an entity's package. The entity may be given in the
// singular or the plural, in any case: Product, Products, product and
// products all give package products and entity Product.
func entityNames(name string) (names, error) {
	words := splitWords(name)
	if len(words) > 0 {
		if last := words[len(words)-1]; singular(last) == last {
			words[len(words)-1] = plural(last)
		}
	}
	return newNames(strings.Join(words, "-"))
}

// project is the Go module the generated code goes into.
type project struct {
	Dir    string // the directory holding go.mod
	Module string // its module path
	Config projectConfig
}

// projectConfig is dygo.json at the project root, written by dygo new
// -template modular. Every field is optional.
type projectConfig struct {
	Layout        string `json:"layout"`
	Main          string `json:"main"`          // the main package for routes, check, start (default ".")
	FeaturesDir   string `json:"featuresDir"`   // where dygo g puts features run from the root: src/modules
	RootModuleDir string `json:"rootModuleDir"` // where features are registered when no closer module exists
	Migrations    string `json:"migrations"`    // the SQL migrations directory (default "migrations")
	Security      bool   `json:"security"`      // dygo g security ran: generators write protected routes and tests that sign in
}

func findProject(dir string) (project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return project{}, err
	}
	for d := dir; ; d = filepath.Dir(d) {
		data, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil {
			mod := modulePath(data)
			if mod == "" {
				return project{}, fmt.Errorf("%s: no module line", filepath.Join(d, "go.mod"))
			}
			p := project{Dir: d, Module: mod}
			if data, err := os.ReadFile(filepath.Join(d, "dygo.json")); err == nil {
				if err := json.Unmarshal(data, &p.Config); err != nil {
					return project{}, fmt.Errorf("%s: %w", filepath.Join(d, "dygo.json"), err)
				}
			}
			return p, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return project{}, err
		}
		if filepath.Dir(d) == d {
			return project{}, fmt.Errorf("no go.mod in %s or any parent directory", dir)
		}
	}
}

// flat reports whether dygo g run in dir writes flat files: in the root of a
// simple project (dygo.json "layout": "simple").
func (p project) flat(dir string) bool {
	return p.Config.Layout == "simple" && dir == p.Dir
}

func modulePath(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module"); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// importPath is the import path of the package in dir.
func (p project) importPath(dir string) string {
	rel, err := filepath.Rel(p.Dir, dir)
	if err != nil || rel == "." {
		return p.Module
	}
	return p.Module + "/" + filepath.ToSlash(rel)
}
