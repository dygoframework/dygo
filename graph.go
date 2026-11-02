package dygo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"strings"
)

type nodeState int

const (
	stateUnbuilt nodeState = iota
	stateBuilding
	stateBuilt
)

// node is one provider registered in one module.
type node struct {
	p          Provider
	mod        *moduleInfo
	controller bool
	out        reflect.Type
	params     []*param
	alias      *dep // target of an Alias provider

	state    nodeState
	instance reflect.Value
	order    int // build order, used to order lifecycle hooks
}

// scopeName is the scope for reports: "request (durable)" for durable providers.
func (n *node) scopeName() string {
	if n.durable() {
		return n.scope().String() + " (durable)"
	}
	return n.scope().String()
}

// durable reports whether n is a durable request scoped provider.
func (n *node) durable() bool {
	if n.p.kind == kindAlias && n.alias.target != nil {
		return n.alias.target.durable()
	}
	return n.scope() == RequestScoped && n.p.durable
}

func (n *node) scope() Scope {
	if n.controller {
		return Singleton
	}
	if n.p.kind == kindAlias && n.alias.target != nil {
		return n.alias.target.scope()
	}
	return n.p.scope
}

func (n *node) String() string {
	return fmt.Sprintf("%s (%s)", typeName(n.out), n.p.src)
}

// moduleInfo is a Module after scanning.
type moduleInfo struct {
	m           *Module
	name        string
	path        string
	imports     []*moduleInfo
	local       map[tokenKey]*node
	exportKeys  map[tokenKey]bool
	exported    map[tokenKey]*node
	providers   []*node
	controllers []*node
	level       int
	ref         *ModuleRef
}

type override struct {
	tok  Token
	p    Provider
	used bool
}

// container holds the scanned module graph and every instance.
type container struct {
	ctx       context.Context
	log       *slog.Logger
	root      *moduleInfo
	modules   []*moduleInfo
	byPtr     map[*Module]*moduleInfo
	globals   []*moduleInfo
	groups    map[string][]*node
	overrides map[tokenKey]*override
	errs      []error
	built     []*node // singletons in build order
	booted    bool
	app       *App // set once every singleton is built

	strategy ContextIDStrategy
	durables durableScopes

	hasRequestScope bool
}

func newContainer(ctx context.Context, log *slog.Logger, overrides []*override) *container {
	c := &container{
		ctx:       ctx,
		log:       log,
		byPtr:     map[*Module]*moduleInfo{},
		groups:    map[string][]*node{},
		overrides: map[tokenKey]*override{},
	}
	for _, o := range overrides {
		c.overrides[o.tok.key()] = o
	}
	return c
}

func (c *container) errorf(format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf(format, args...))
}

// build scans and validates the graph. It returns false when errors were found.
func (c *container) build(root *Module) bool {
	if root == nil {
		c.errorf("root module is nil")
		return false
	}
	c.root = c.scan(root, nil)
	if len(c.errs) > 0 {
		return false // the module tree itself is broken
	}
	for _, mi := range c.modules {
		c.register(mi)
	}
	for _, o := range c.overrides {
		if !o.used {
			c.errorf("override for %s: no module provides this token", o.tok)
		}
	}
	for _, mi := range c.modules {
		c.checkExports(mi)
	}
	for _, mi := range c.modules {
		for _, n := range slices.Concat(mi.providers, mi.controllers) {
			c.resolveDeps(n)
		}
	}
	c.checkScopes()
	c.checkCycles()
	c.computeLevels()
	return len(c.errs) == 0
}

// scan walks the import tree depth first and detects import cycles.
func (c *container) scan(m *Module, stack []*moduleInfo) *moduleInfo {
	if mi, ok := c.byPtr[m]; ok {
		if slices.Contains(stack, mi) {
			names := make([]string, 0, len(stack)+1)
			for _, s := range stack[slices.Index(stack, mi):] {
				names = append(names, s.name)
			}
			c.errorf("module import cycle: %s > %s", strings.Join(names, " > "), mi.name)
		}
		return mi
	}
	mi := &moduleInfo{
		m:          m,
		name:       m.Name,
		local:      map[tokenKey]*node{},
		exportKeys: map[tokenKey]bool{},
		exported:   map[tokenKey]*node{},
	}
	if mi.name == "" {
		mi.name = fmt.Sprintf("<unnamed module #%d>", len(c.modules)+1)
		c.errorf("%s: Module.Name is required", mi.name)
	}
	names := make([]string, 0, len(stack)+1)
	for _, s := range stack {
		names = append(names, s.name)
	}
	mi.path = strings.Join(append(names, mi.name), " > ")
	mi.ref = &ModuleRef{c: c, mi: mi}
	c.byPtr[m] = mi
	c.modules = append(c.modules, mi)
	if m.Global {
		c.globals = append(c.globals, mi)
	}
	stack = append(stack, mi)
	for i, imp := range m.Imports {
		if imp == nil {
			c.errorf("module %q: Imports[%d] is nil (check for an import initialization cycle between packages)", mi.name, i)
			continue
		}
		mi.imports = append(mi.imports, c.scan(imp, stack))
	}
	return mi
}

// newNode analyzes a provider for a module.
func (c *container) newNode(mi *moduleInfo, p Provider, controller bool) (*node, []error) {
	n := &node{p: p, mod: mi, controller: controller, out: p.outType()}
	if p.err != nil {
		return nil, []error{fmt.Errorf("module %q: %w", mi.name, p.err)}
	}
	var errs []error
	switch p.kind {
	case kindConstructor:
		params, perrs := analyzeParams(p.fn.Type())
		for _, err := range perrs {
			errs = append(errs, fmt.Errorf("module %q: %s: %w", mi.name, p.src, err))
		}
		n.params = params
	case kindAlias:
		n.alias = &dep{kind: depPlain, typ: p.target.Type(), key: p.target.key(), field: -1, label: "alias target"}
		if !p.target.Type().AssignableTo(n.out) {
			errs = append(errs, fmt.Errorf("module %q: alias %s: target %s is not assignable to %s",
				mi.name, p.tokens[0], p.target, typeName(n.out)))
		}
	}
	if p.scope != Singleton && p.scope != Transient && p.scope != RequestScoped {
		errs = append(errs, fmt.Errorf("module %q: %s: unknown scope %v", mi.name, n, p.scope))
	}
	if controller && p.scope != Singleton {
		errs = append(errs, fmt.Errorf("module %q: controller %s must be a singleton; inject request scoped values with dygo.Scoped[T]",
			mi.name, n))
	}
	return n, errs
}

// addNode is newNode for registration: errors go to the container.
func (c *container) addNode(mi *moduleInfo, p Provider, controller bool) *node {
	n, errs := c.newNode(mi, p, controller)
	c.errs = append(c.errs, errs...)
	return n
}

// register creates nodes for a module's providers and controllers.
func (c *container) register(mi *moduleInfo) {
	overridden := map[tokenKey]*node{}
	for _, p := range mi.m.Providers {
		n := c.addNode(mi, p, false)
		if n == nil {
			continue
		}
		keys := p.registeredKeys()
		mapped := 0
		for _, k := range keys {
			if !n.out.AssignableTo(k.typ) {
				c.errorf("module %q: %s cannot be registered as %s: type %s does not implement or match it",
					mi.name, n, k, typeName(n.out))
				continue
			}
			target := n
			if o, ok := c.overrides[k]; ok {
				o.used = true
				if overridden[k] == nil {
					on := c.addNode(mi, o.p, false)
					if on == nil {
						continue
					}
					if !on.out.AssignableTo(k.typ) {
						c.errorf("override for %s: %s is not assignable to it", k, typeName(on.out))
						continue
					}
					overridden[k] = on
					mi.providers = append(mi.providers, on)
				}
				target = overridden[k]
			}
			if prev, dup := mi.local[k]; dup && prev != target {
				c.errorf("module %q: %s is provided twice (%s and %s)", mi.name, k, prev.p.src, n.p.src)
				continue
			}
			mi.local[k] = target
			if target == n {
				mapped++
			}
		}
		// A provider whose every token is overridden is never built, so a test
		// can replace a provider whose real dependencies are unavailable.
		if mapped > 0 || len(keys) == 0 || len(p.groups) > 0 {
			mi.providers = append(mi.providers, n)
		}
		for _, g := range p.groups {
			c.groups[g] = append(c.groups[g], n)
		}
	}
	for _, p := range mi.m.Controllers {
		if n := c.addNode(mi, p, true); n != nil {
			mi.controllers = append(mi.controllers, n)
		}
	}
	for _, t := range mi.m.Exports {
		if t == nil {
			c.errorf("module %q: Exports contains a nil token", mi.name)
			continue
		}
		mi.exportKeys[t.key()] = true
	}
}

// exportedNode returns the node module mi exports for k, or nil.
func (c *container) exportedNode(mi *moduleInfo, k tokenKey) *node {
	if !mi.exportKeys[k] {
		return nil
	}
	if n, ok := mi.exported[k]; ok {
		return n
	}
	n := mi.local[k]
	if n == nil {
		n, _ = c.fromImports(mi, k)
	}
	mi.exported[k] = n
	return n
}

// fromImports finds k among the exports of mi's imports.
func (c *container) fromImports(mi *moduleInfo, k tokenKey) (*node, []*moduleInfo) {
	var found *node
	var from []*moduleInfo
	for _, imp := range mi.imports {
		n := c.exportedNode(imp, k)
		switch {
		case n == nil:
		case found == nil:
			found, from = n, []*moduleInfo{imp}
		case n != found:
			from = append(from, imp)
		}
	}
	return found, from
}

func (c *container) checkExports(mi *moduleInfo) {
	for _, t := range mi.m.Exports {
		if t != nil && c.exportedNode(mi, t.key()) == nil {
			c.errorf("module %q exports %s but neither provides it nor imports a module that exports it", mi.name, t)
		}
	}
}

// visible finds the provider for k as seen from module mi: its own providers
// first, then its imports' exports, then global modules' exports.
func (c *container) visible(mi *moduleInfo, k tokenKey) (*node, error) {
	if n := mi.local[k]; n != nil {
		return n, nil
	}
	if n, from := c.fromImports(mi, k); n != nil {
		if len(from) > 1 {
			return nil, fmt.Errorf("%s is exported by more than one imported module (%s)", k, moduleNames(from))
		}
		return n, nil
	}
	var found *node
	var from []*moduleInfo
	for _, g := range c.globals {
		if g == mi {
			continue
		}
		if n := c.exportedNode(g, k); n != nil {
			if found != nil && n != found {
				from = append(from, g)
				return nil, fmt.Errorf("%s is exported by more than one global module (%s)", k, moduleNames(from))
			}
			found = n
			from = append(from, g)
		}
	}
	return found, nil
}

func moduleNames(ms []*moduleInfo) string {
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = fmt.Sprintf("%q", m.name)
	}
	return strings.Join(names, ", ")
}

// resolveDeps links every dependency of n to its provider and records errors.
func (c *container) resolveDeps(n *node) {
	c.errs = append(c.errs, c.link(n)...)
}

func (c *container) link(n *node) []error {
	var errs []error
	resolve := func(d *dep) {
		switch {
		case d.lookupKey():
			t, err := c.visible(n.mod, d.key)
			if err != nil {
				errs = append(errs, fmt.Errorf("module %q: %s needs %s (%s): %w", n.mod.name, n, d.key, d.label, err))
				return
			}
			if t == nil && !d.isOptional() {
				errs = append(errs, c.missingError(n, d))
			}
			d.target = t
		case d.kind == depGroup:
			elem := d.typ.Elem()
			for _, m := range c.groups[d.group] {
				if !m.out.AssignableTo(elem) {
					errs = append(errs, fmt.Errorf("module %q: %s needs group %q as %s, but member %s does not match",
						n.mod.name, n, d.group, d.typ, m))
					continue
				}
				d.members = append(d.members, m)
			}
		}
	}
	if n.alias != nil {
		resolve(n.alias)
	}
	for _, p := range n.params {
		for _, d := range p.deps {
			resolve(d)
		}
	}
	return errs
}

// checkScopes enforces the request scope rule: only request scoped providers
// may inject request scoped providers or *http.Request directly.
func (c *container) checkScopes() {
	for _, mi := range c.modules {
		for _, n := range slices.Concat(mi.providers, mi.controllers) {
			if n.p.durable && n.scope() != RequestScoped {
				c.errorf("module %q: %s is dygo.Durable but %s; durable needs dygo.WithScope(dygo.RequestScoped)", mi.name, n, n.scope())
			}
			if n.durable() {
				c.checkDurable(mi, n)
			}
			if n.scope() == RequestScoped {
				c.hasRequestScope = true
				continue
			}
			for _, p := range n.params {
				for _, d := range p.deps {
					switch {
					case d.kind == depRequest:
						c.errorf("module %q: %s injects *http.Request, which is only available to dygo.RequestScoped providers",
							mi.name, n)
					case d.kind == depContextID:
						c.errorf("module %q: %s injects dygo.ContextID, which is only available to dygo.RequestScoped providers",
							mi.name, n)
					case d.kind == depGroup:
						for _, m := range d.members {
							if m.scope() == RequestScoped {
								c.errorf("module %q: %s (%s) injects group %q, whose member %s is request scoped",
									mi.name, n, n.scope(), d.group, m)
							}
						}
					case d.kind != depScoped && d.target != nil && d.target.scope() == RequestScoped:
						c.errorf("module %q: %s is %s but injects request scoped %s (%s). "+
							"Inject dygo.Scoped[%s] and call Get(ctx), or make it dygo.RequestScoped too",
							mi.name, n, n.scope(), d.target, d.label, typeName(d.key.typ))
					}
				}
			}
		}
	}
}

// checkDurable enforces what a durable provider may inject: its instance
// outlives the request, so neither the request nor per-request providers.
func (c *container) checkDurable(mi *moduleInfo, n *node) {
	for _, p := range n.params {
		for _, d := range p.deps {
			switch {
			case d.kind == depRequest:
				c.errorf("module %q: durable %s injects *http.Request, but its instance is shared by every request of a context ID; "+
					"inject dygo.ContextID and put what it needs in the payload", mi.name, n)
			case d.kind == depGroup:
				for _, m := range d.members {
					if m.scope() == RequestScoped && !m.durable() {
						c.errorf("module %q: durable %s injects group %q, whose member %s is request scoped but not durable",
							mi.name, n, d.group, m)
					}
				}
			case d.kind != depScoped && d.target != nil && d.target.scope() == RequestScoped && !d.target.durable():
				c.errorf("module %q: durable %s injects %s (%s), which is request scoped but not durable. "+
					"Make it dygo.Durable too, or inject dygo.Scoped[%s]", mi.name, n, d.target, d.label, typeName(d.key.typ))
			}
		}
	}
}

// missingError explains a missing dependency and, when another module has it,
// how to make it visible.
func (c *container) missingError(n *node, d *dep) error {
	msg := fmt.Sprintf("module %q: %s needs %s (%s), but no provider for it is visible in %q",
		n.mod.name, n, d.key, d.label, n.mod.name)
	var owners []*moduleInfo
	for _, mi := range c.modules {
		if mi.local[d.key] != nil {
			owners = append(owners, mi)
		}
	}
	switch {
	case len(owners) == 0:
		msg += ". No module provides it; add it to Providers"
	case len(owners) == 1:
		o := owners[0]
		if !o.exportKeys[d.key] {
			msg += fmt.Sprintf(". It is provided by module %q: add it to that module's Exports and import %q into %q", o.name, o.name, n.mod.name)
		} else {
			msg += fmt.Sprintf(". It is exported by module %q: import %q into %q", o.name, o.name, n.mod.name)
		}
	default:
		msg += fmt.Sprintf(". It is provided by modules %s; import the one you need", moduleNames(owners))
	}
	return fmt.Errorf("%s (module path: %s)", msg, n.mod.path)
}

// edges lists the nodes n needs before it can be built. Lazy dependencies are
// not edges, which is what lets them break cycles.
func (n *node) edges() []*node {
	var out []*node
	if n.alias != nil && n.alias.target != nil {
		out = append(out, n.alias.target)
	}
	for _, p := range n.params {
		for _, d := range p.deps {
			switch {
			case d.kind == depGroup:
				out = append(out, d.members...)
			case d.kind != depLazy && d.kind != depScoped && d.target != nil:
				out = append(out, d.target)
			}
		}
	}
	return out
}

func (c *container) checkCycles() {
	color := map[*node]int{} // 0 white, 1 grey, 2 black
	var stack []*node
	reported := map[*node]bool{}
	var visit func(n *node)
	visit = func(n *node) {
		color[n] = 1
		stack = append(stack, n)
		for _, e := range n.edges() {
			switch color[e] {
			case 0:
				visit(e)
			case 1:
				cycle := stack[slices.Index(stack, e):]
				if reported[e] {
					continue
				}
				reported[e] = true
				parts := make([]string, 0, len(cycle)+1)
				for _, s := range cycle {
					parts = append(parts, fmt.Sprintf("%s in %q", typeName(s.out), s.mod.name))
				}
				parts = append(parts, typeName(e.out))
				c.errorf("dependency cycle: %s. Break it by injecting one side as dygo.Lazy[T]", strings.Join(parts, " -> "))
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = 2
	}
	for _, mi := range c.modules {
		for _, n := range slices.Concat(mi.providers, mi.controllers) {
			if color[n] == 0 {
				visit(n)
			}
		}
	}
}

// computeLevels sets each module's level: 0 for modules that import nothing,
// otherwise one more than the highest module it depends on. Non-global modules
// implicitly depend on global modules. Lifecycle hooks run level by level.
func (c *container) computeLevels() {
	done := map[*moduleInfo]bool{}
	visiting := map[*moduleInfo]bool{}
	var level func(mi *moduleInfo) int
	level = func(mi *moduleInfo) int {
		if done[mi] {
			return mi.level
		}
		visiting[mi] = true
		deps := slices.Clone(mi.imports)
		if !mi.m.Global {
			deps = append(deps, c.globals...)
		}
		lv := 0
		for _, d := range deps {
			if d == mi || visiting[d] {
				continue
			}
			lv = max(lv, level(d)+1)
		}
		visiting[mi] = false
		done[mi] = true
		mi.level = lv
		return lv
	}
	for _, mi := range c.modules {
		level(mi)
	}
}

// modulesByLevel returns modules ordered for initialization.
func (c *container) modulesByLevel() []*moduleInfo {
	ms := slices.Clone(c.modules)
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].level < ms[j].level })
	return ms
}

func (c *container) bootError() error {
	if len(c.errs) == 0 {
		return nil
	}
	return &BootError{Problems: c.errs}
}

// BootError lists every problem found while building the application. It is
// returned by [CreateApplicationContext] instead of stopping at the first one.
type BootError struct {
	Problems []error
}

func (e *BootError) Error() string {
	var b strings.Builder
	if len(e.Problems) == 1 {
		b.WriteString("dygo: 1 problem building the application:")
	} else {
		fmt.Fprintf(&b, "dygo: %d problems building the application:", len(e.Problems))
	}
	for i, p := range e.Problems {
		fmt.Fprintf(&b, "\n  %d. %v", i+1, p)
	}
	return b.String()
}

// Unwrap returns the individual problems, for errors.Is and errors.As.
func (e *BootError) Unwrap() []error { return e.Problems }

var (
	// ErrNotFound is returned by Get when no provider matches the token.
	ErrNotFound = errors.New("dygo: no provider found")
	// ErrAmbiguous is returned by Get when more than one provider matches.
	ErrAmbiguous = errors.New("dygo: more than one provider found")
)
