package dygo

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"slices"
)

// GraphInfo describes a module graph: every module and every provider with
// its resolved dependencies. [Describe] returns it, and the dygo CLI renders
// it (dygo graph) and checks it (dygo check).
type GraphInfo struct {
	Modules   []GraphModule   `json:"modules"`
	Providers []GraphProvider `json:"providers"`
}

// GraphModule is one module. Imports holds indexes into GraphInfo.Modules.
// The root module is first.
type GraphModule struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Global  bool     `json:"global,omitempty"`
	Level   int      `json:"level"`
	Imports []int    `json:"imports,omitempty"`
	Exports []string `json:"exports,omitempty"`
}

// GraphProvider is one provider or controller. ID is its index in
// GraphInfo.Providers and Module its module's index in GraphInfo.Modules.
type GraphProvider struct {
	ID         int        `json:"id"`
	Module     int        `json:"module"`
	Type       string     `json:"type"`
	Tokens     []string   `json:"tokens,omitempty"`
	Source     string     `json:"source"`
	Scope      string     `json:"scope"`
	Controller bool       `json:"controller,omitempty"`
	Groups     []string   `json:"groups,omitempty"`
	Deps       []GraphDep `json:"deps,omitempty"`
}

// GraphDep is one dependency of a provider. Kind is empty for a plain
// dependency, otherwise optional, lazy, scoped, group, context, moduleRef or
// request. Providers lists the provider IDs it resolved to: one for a token,
// every member for a group, none when an optional dependency is missing.
type GraphDep struct {
	Token     string `json:"token"`
	Kind      string `json:"kind,omitempty"`
	Providers []int  `json:"providers,omitempty"`
}

// Describe scans and validates the module tree from root, like
// [CreateApplicationContext], without building anything. It is the cheapest
// wiring check, for a unit test or CI:
//
//	func TestWiring(t *testing.T) {
//		if _, err := dygo.Describe(app.Module); err != nil {
//			t.Fatal(err)
//		}
//	}
//
// On a [*BootError] the graph is still returned when the module tree could be
// scanned, with unresolved dependencies left empty.
func Describe(root *Module, opts ...Option) (*GraphInfo, error) {
	o := newOptions(opts)
	c := newContainer(context.Background(), o.log, o.overrides)
	if !c.build(root) {
		if c.root == nil {
			return nil, c.bootError()
		}
		return c.describe(), c.bootError()
	}
	return c.describe(), nil
}

var depKindNames = [...]string{
	depPlain: "", depOptional: "optional", depLazy: "lazy", depGroup: "group",
	depContext: "context", depModuleRef: "moduleRef", depScoped: "scoped", depRequest: "request", depContextID: "contextID",
}

func (c *container) describe() *GraphInfo {
	g := &GraphInfo{}
	mods := map[*moduleInfo]int{}
	for i, mi := range c.modules {
		mods[mi] = i
	}
	ids := map[*node]int{}
	var nodes []*node
	for _, mi := range c.modules {
		for _, n := range slices.Concat(mi.providers, mi.controllers) {
			ids[n] = len(nodes)
			nodes = append(nodes, n)
		}
	}
	for _, mi := range c.modules {
		gm := GraphModule{Name: mi.name, Path: mi.path, Global: mi.m.Global, Level: mi.level}
		for _, imp := range mi.imports {
			gm.Imports = append(gm.Imports, mods[imp])
		}
		for _, t := range mi.m.Exports {
			if t != nil {
				gm.Exports = append(gm.Exports, t.String())
			}
		}
		g.Modules = append(g.Modules, gm)
	}
	ref := func(targets ...*node) []int {
		var out []int
		for _, t := range targets {
			if id, ok := ids[t]; ok {
				out = append(out, id)
			}
		}
		return out
	}
	for id, n := range nodes {
		gp := GraphProvider{
			ID: id, Module: mods[n.mod], Type: typeName(n.out), Source: n.p.src,
			Scope: n.scopeName(), Controller: n.controller, Groups: n.p.groups,
		}
		if !n.controller {
			for _, k := range n.p.registeredKeys() {
				gp.Tokens = append(gp.Tokens, k.String())
			}
		}
		addDep := func(d *dep) {
			gd := GraphDep{Token: d.key.String(), Kind: depKindNames[d.kind]}
			switch d.kind {
			case depGroup:
				gd.Token = d.group
				gd.Providers = ref(d.members...)
			case depContext, depModuleRef, depRequest, depContextID:
				gd.Token = typeName(d.typ)
			default:
				if d.target != nil {
					gd.Providers = ref(d.target)
				}
			}
			gp.Deps = append(gp.Deps, gd)
		}
		if n.alias != nil {
			addDep(n.alias)
		}
		for _, p := range n.params {
			for _, d := range p.deps {
				addDep(d)
			}
		}
		g.Providers = append(g.Providers, gp)
	}
	return g
}

// ErrInspected is returned by [Create] and [CreateApplicationContext] when the
// DYGO_INSPECT_OUT environment variable is set: the dygo CLI (dygo routes,
// graph and check) runs the application that way to read its graph and
// routes, and the application is not started.
var ErrInspected = errors.New("dygo: inspection written to DYGO_INSPECT_OUT; the application was not started")

// inspectReport is the JSON file written for the dygo CLI (see [ErrInspected]).
// It is not public API: cmd/dygo keeps its own copy of these types, and the
// format may change in any release.
type inspectReport struct {
	Graph  *GraphInfo     `json:"graph,omitempty"`
	Routes []routeSummary `json:"routes,omitempty"`
	Errors []string       `json:"errors,omitempty"`
}

// routeSummary is a [RouteInfo] with type names instead of reflect.Types.
type routeSummary struct {
	Method     string `json:"method"`
	Host       string `json:"host,omitempty"`
	Path       string `json:"path"`
	Version    string `json:"version,omitempty"`
	Controller string `json:"controller"`
	Module     string `json:"module"`
	Handler    string `json:"handler"`
	Input      string `json:"input,omitempty"`
	Output     string `json:"output,omitempty"`
	Status     int    `json:"status,omitempty"`
}

// inspection is set from the environment by the dygo CLI: DYGO_INSPECT_OUT
// names the report file, and DYGO_INSPECT=routes also builds the providers
// and compiles routes (otherwise only the graph is validated, nothing runs).
type inspection struct {
	out    string
	routes bool
}

func inspectionFromEnv() *inspection {
	out := os.Getenv("DYGO_INSPECT_OUT")
	if out == "" {
		return nil
	}
	return &inspection{out: out, routes: os.Getenv("DYGO_INSPECT") == "routes"}
}

// write saves the report. It returns the boot error when there is one, so a
// broken app still fails as usual, and ErrInspected otherwise.
func (in *inspection) write(log *slog.Logger, c *container, routes []RouteInfo, bootErr error) error {
	rep := inspectReport{}
	if c.root != nil {
		rep.Graph = c.describe()
	}
	for _, r := range routes {
		rep.Routes = append(rep.Routes, routeSummary{
			Method: r.Method, Host: r.Host, Path: r.Path, Version: r.Version,
			Controller: r.Controller, Module: r.Module, Handler: r.Handler,
			Input: typeNameOrEmpty(r.Input), Output: typeNameOrEmpty(r.Output), Status: r.Status,
		})
	}
	var be *BootError
	if errors.As(bootErr, &be) {
		for _, p := range be.Problems {
			rep.Errors = append(rep.Errors, p.Error())
		}
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err == nil {
		err = os.WriteFile(in.out, data, 0o600)
	}
	if err != nil {
		return errors.Join(bootErr, err)
	}
	log.Info("dygo: inspection written", "file", in.out)
	if bootErr != nil {
		return bootErr
	}
	return ErrInspected
}

func typeNameOrEmpty(t reflect.Type) string {
	if t == nil {
		return ""
	}
	return t.String()
}
