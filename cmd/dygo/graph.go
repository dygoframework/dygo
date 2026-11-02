package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/dygoframework/dygo"
)

func cmdGraph(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("graph")
	format := fs.String("format", "text", "text, mermaid, dot or json")
	pos, _, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	pkg, err := pkgArg(pos)
	if err != nil {
		return err
	}
	render, ok := map[string]func(io.Writer, *dygo.GraphInfo){
		"text": graphText, "mermaid": graphMermaid, "dot": graphDOT,
		"json": func(w io.Writer, g *dygo.GraphInfo) { _ = writeJSON(w, g) },
	}[*format]
	if !ok {
		return fmt.Errorf("unknown format %q: want text, mermaid, dot or json", *format)
	}
	rep, err := inspect(pkg, false)
	if err != nil {
		return err
	}
	if len(rep.Errors) > 0 {
		return printErrors(stderr, rep)
	}
	render(stdout, rep.Graph)
	return nil
}

// depLabel is how a dependency reads in text output: the token, with its kind.
func depLabel(d dygo.GraphDep) string {
	switch d.Kind {
	case "":
		return d.Token
	case "group":
		return "group " + strconv.Quote(d.Token)
	}
	return d.Kind + " " + d.Token
}

// graphText prints modules from the root, each with its imports, exports,
// providers and controllers.
func graphText(w io.Writer, g *dygo.GraphInfo) {
	for i, m := range g.Modules {
		if i > 0 {
			fmt.Fprintln(w)
		}
		flags := []string{"level " + strconv.Itoa(m.Level)}
		if i == 0 {
			flags = append(flags, "root")
		}
		if m.Global {
			flags = append(flags, "global")
		}
		fmt.Fprintf(w, "%s (%s)\n", m.Name, strings.Join(flags, ", "))
		if len(m.Imports) > 0 {
			var names []string
			for _, imp := range m.Imports {
				names = append(names, g.Modules[imp].Name)
			}
			fmt.Fprintf(w, "  imports     %s\n", strings.Join(names, ", "))
		}
		if len(m.Exports) > 0 {
			fmt.Fprintf(w, "  exports     %s\n", strings.Join(m.Exports, ", "))
		}
		for _, p := range g.Providers {
			if p.Module != i {
				continue
			}
			kind := "provider  "
			if p.Controller {
				kind = "controller"
			}
			extra := ""
			if p.Scope != "singleton" {
				extra = " [" + p.Scope + "]"
			}
			fmt.Fprintf(w, "  %s  %s%s  (%s)\n", kind, p.Type, extra, p.Source)
			for _, d := range p.Deps {
				fmt.Fprintf(w, "                <- %s\n", depLabel(d))
			}
		}
	}
}

// graphMermaid prints a flowchart: one subgraph per module, an arrow from each
// provider to what it injects, and dotted arrows for module imports.
func graphMermaid(w io.Writer, g *dygo.GraphInfo) {
	fmt.Fprintln(w, "flowchart LR")
	for i, m := range g.Modules {
		fmt.Fprintf(w, "  subgraph m%d[%q]\n", i, m.Name)
		for _, p := range g.Providers {
			if p.Module == i {
				shape := "[%q]"
				if p.Controller {
					shape = "([%q])"
				}
				fmt.Fprintf(w, "    p%d"+shape+"\n", p.ID, mermaidText(p.Type))
			}
		}
		fmt.Fprintln(w, "  end")
	}
	for i, m := range g.Modules {
		for _, imp := range m.Imports {
			fmt.Fprintf(w, "  m%d -.-> m%d\n", i, imp)
		}
	}
	for _, p := range g.Providers {
		for _, d := range p.Deps {
			for _, t := range d.Providers {
				fmt.Fprintf(w, "  p%d --> p%d\n", p.ID, t)
			}
		}
	}
}

// mermaidText escapes characters Mermaid labels do not accept.
func mermaidText(s string) string {
	return strings.NewReplacer(`"`, "#quot;", "[", "#91;", "]", "#93;").Replace(s)
}

// graphDOT prints Graphviz input: dot -Tsvg graph.dot > graph.svg
func graphDOT(w io.Writer, g *dygo.GraphInfo) {
	fmt.Fprintln(w, "digraph dygo {")
	fmt.Fprintln(w, "  rankdir=LR;")
	fmt.Fprintln(w, "  node [shape=box, fontname=Helvetica];")
	for i, m := range g.Modules {
		fmt.Fprintf(w, "  subgraph cluster_%d {\n    label=%q;\n", i, m.Name)
		for _, p := range g.Providers {
			if p.Module == i {
				attrs := ""
				if p.Controller {
					attrs = ", style=rounded"
				}
				fmt.Fprintf(w, "    p%d [label=%q%s];\n", p.ID, p.Type, attrs)
			}
		}
		fmt.Fprintln(w, "  }")
	}
	for _, p := range g.Providers {
		for _, d := range p.Deps {
			for _, t := range d.Providers {
				style := ""
				if d.Kind != "" {
					style = fmt.Sprintf(" [style=dashed, label=%q]", d.Kind)
				}
				fmt.Fprintf(w, "  p%d -> p%d%s;\n", p.ID, t, style)
			}
		}
	}
	fmt.Fprintln(w, "}")
}
