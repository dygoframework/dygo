package dygo

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/internal/pattern"
)

// RouteInfo describes one registered route.
type RouteInfo struct {
	// Method is the HTTP method, or "" for a route registered with All.
	Method string
	// Host is the host restriction, if any.
	Host string
	// Path is the full ServeMux path pattern, including the global prefix
	// and URI version.
	Path string
	// Version is the API version the route serves, if versioning is on.
	Version string
	// Controller and Module identify where the route was declared.
	Controller string
	Module     string
	// Handler is the handler function name.
	Handler string
	// Input and Output are the In and Out types of a typed handler, nil for
	// HandleRaw. Tools such as package openapi read them.
	Input, Output reflect.Type
	// Status is the success status set with HTTPCode, or 0 for the default.
	Status int

	rc *routeConfig
}

// Metadata returns the route's metadata for key, as ExecutionContext.MergedMetadata
// does: slices set on both the controller and the route joined, otherwise the
// route's value, otherwise the controller's.
func (ri RouteInfo) Metadata(key any) (any, bool) {
	if ri.rc == nil {
		return nil, false
	}
	return ri.rc.mergedMetadata(key)
}

func (ri RouteInfo) String() string {
	m := ri.Method
	if m == "" {
		m = "ALL"
	}
	s := fmt.Sprintf("%s %s%s", m, ri.Host, ri.Path)
	switch ri.Version {
	case "":
	case VersionNeutral:
		s += " (any version)"
	default:
		s += " (v" + ri.Version + ")"
	}
	return s
}

func (ri RouteInfo) source() string {
	if ri.Handler == "" {
		return fmt.Sprintf("controller %s in module %q", ri.Controller, ri.Module)
	}
	return fmt.Sprintf("%s, controller %s in module %q", ri.Handler, ri.Controller, ri.Module)
}

// compiledRoute is a route ready to serve.
type compiledRoute struct {
	info      RouteInfo
	rc        *routeConfig
	ep        *endpoint
	pl        pipeline
	ctrl      reflect.Type
	app       *HTTPApp
	bodyLimit int64
	handler   http.Handler // ServeHTTP wrapped in route middleware
}

// muxEntry is one pattern to register on the ServeMux.
type muxEntry struct {
	method, host, path string
	routes             []*compiledRoute // more than one only for non-URI versioning
}

// buildRoutes asks every controller for its routes, compiles them, checks the
// table and builds the application handler.
func (a *HTTPApp) buildRoutes() []error {
	o := a.opts.http
	var defs []*routeDef
	er, errs := newEnhancerResolver(a.App, &a.opts.http.global, true)
	for _, n := range a.c.built {
		if !n.controller {
			continue
		}
		ctrl, ok := n.instance.Interface().(Controller)
		if !ok {
			errs = append(errs, fmt.Errorf("module %q: controller %s has no Routes(r dygo.Router) method", n.mod.name, n.out))
			continue
		}
		r := &router{defs: &defs, ctrl: ctrlInfo{typ: n.out, mi: n.mod}}
		if err := callRoutes(ctrl, r); err != nil {
			errs = append(errs, fmt.Errorf("module %q: %s.Routes: %w", n.mod.name, n.out, err))
		}
	}

	var routes []*compiledRoute
	var entries []*muxEntry
	for _, d := range defs {
		rs, es, err := a.compileDef(d, er)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		routes = append(routes, rs...)
		entries = append(entries, es...)
	}

	entries, dupErrs := mergeEntries(entries, o.versioning)
	errs = append(errs, dupErrs...)

	mux := http.NewServeMux()
	for _, e := range entries {
		if err := register(mux, e, a, o.versioning); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs
	}
	// Unknown paths get a JSON 404 unless the app has its own catch-all.
	_ = safeHandle(mux, "/", http.HandlerFunc(a.notFound))

	var h http.Handler = mux
	for i := len(o.middleware) - 1; i >= 0; i-- {
		h = o.middleware[i](h)
	}
	a.handler = h
	for _, r := range routes {
		a.routes = append(a.routes, r.info)
	}
	slices.SortFunc(a.routes, func(x, y RouteInfo) int {
		return cmp.Or(strings.Compare(x.Path, y.Path), strings.Compare(x.Method, y.Method), strings.Compare(x.Version, y.Version))
	})
	for _, r := range a.routes {
		a.c.log.Debug("dygo: mapped route", "route", r.String(), "handler", r.Handler)
	}
	return nil
}

func callRoutes(ctrl Controller, r Router) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panicked: %v", p)
		}
	}()
	ctrl.Routes(r)
	return nil
}

// compileDef turns one Router call into compiled routes and mux entries
// (several for URI versioning).
func (a *HTTPApp) compileDef(d *routeDef, er *enhancerResolver) ([]*compiledRoute, []*muxEntry, error) {
	o := a.opts.http
	rc := &routeConfig{level: levelController}
	for _, opt := range d.ctrlOpts {
		if opt != nil {
			opt.applyRoute(rc)
		}
	}
	rc.level = levelRoute
	for _, opt := range d.opts {
		if opt != nil {
			opt.applyRoute(rc)
		}
	}
	rc.mergeMeta()
	info := RouteInfo{Method: d.method, Host: d.host, Controller: d.ctrl.typ.String(), Module: d.ctrl.mi.name}
	if d.handler == nil {
		info.Path = pattern.Join(append(d.prefixes, d.path)...)
		return nil, nil, fmt.Errorf("route %s (%s): handler is nil", info, info.source())
	}
	info.Handler = d.handler.name()
	info.Input, info.Output = d.handler.types()
	info.Status, info.rc = rc.code, rc

	versions := d.versions
	if rc.versions != nil {
		versions = rc.versions
	}
	if versions == nil && o.versioning != nil {
		versions = o.versioning.defaults
	}

	local := pattern.Join(append(slices.Clone(d.prefixes), d.path)...)
	global := ""
	if o.globalPrefix != "" && !excluded(local, o.prefixExclude) {
		global = o.globalPrefix
	}
	fail := func(err error) ([]*compiledRoute, []*muxEntry, error) {
		info.Path = pattern.Join(global, local)
		return nil, nil, fmt.Errorf("route %s (%s): %w", info, info.source(), err)
	}
	if o.versioning == nil && len(versions) == 1 && versions[0] == VersionNeutral {
		versions = nil // a version neutral route needs no versioning
	}
	if len(versions) > 0 && o.versioning == nil {
		return fail(fmt.Errorf("route has versions %v but versioning is off; add dygo.WithVersioning", versions))
	}
	if err := pattern.Validate(local); err != nil {
		return fail(err)
	}
	pl, err := er.route(d.ctrl.mi, rc)
	if err != nil {
		return fail(err)
	}
	ep, err := d.handler.endpoint(rc, pattern.Wildcards(local), pl.pipes)
	if err != nil {
		return fail(err)
	}
	limit := o.bodyLimit
	if rc.bodyLimit != nil {
		limit = *rc.bodyLimit
	}
	mk := func(path, version string) *compiledRoute {
		ri := info
		ri.Path, ri.Version = path, version
		cr := &compiledRoute{info: ri, rc: rc, ep: ep, pl: pl, ctrl: d.ctrl.typ, app: a, bodyLimit: limit}
		var h http.Handler = cr
		for i := len(rc.middleware) - 1; i >= 0; i-- {
			h = rc.middleware[i](h)
		}
		cr.handler = h
		return cr
	}

	var routes []*compiledRoute
	var entries []*muxEntry
	switch {
	case len(versions) == 0:
		cr := mk(pattern.Join(global, local), "")
		routes = append(routes, cr)
		entries = append(entries, &muxEntry{method: d.method, host: d.host, path: cr.info.Path, routes: []*compiledRoute{cr}})
	case o.versioning.kind == versionURI:
		for _, v := range versions {
			p := pattern.Join(global, "v"+v, local)
			if v == VersionNeutral {
				p = pattern.Join(global, local)
			}
			cr := mk(p, v)
			routes = append(routes, cr)
			entries = append(entries, &muxEntry{method: d.method, host: d.host, path: p, routes: []*compiledRoute{cr}})
		}
	default:
		p := pattern.Join(global, local)
		e := &muxEntry{method: d.method, host: d.host, path: p}
		for _, v := range versions {
			cr := mk(p, v)
			routes = append(routes, cr)
			e.routes = append(e.routes, cr)
		}
		entries = append(entries, e)
	}
	return routes, entries, nil
}

func excluded(path string, exclude []string) bool {
	for _, e := range exclude {
		e = pattern.Join(e)
		if path == e || strings.HasPrefix(path, strings.TrimSuffix(e, "/{$}")+"/") {
			return true
		}
	}
	return false
}

// mergeEntries reports duplicate routes. With header, media type or custom
// versioning, routes that share a pattern but differ in version are merged
// into one entry served by a version dispatcher.
func mergeEntries(entries []*muxEntry, v *VersioningOptions) ([]*muxEntry, []error) {
	var errs []error
	byKey := map[string]*muxEntry{}
	var out []*muxEntry
	for _, e := range entries {
		key := e.method + " " + e.host + pattern.Normalize(e.path)
		prev, ok := byKey[key]
		if !ok {
			byKey[key] = e
			out = append(out, e)
			continue
		}
		dispatch := v != nil && v.kind != versionURI && prev.routes[0].info.Version != "" && e.routes[0].info.Version != ""
		if !dispatch {
			errs = append(errs, fmt.Errorf("route %s is registered twice: %s and %s",
				e.routes[0].info, prev.routes[0].info.source(), e.routes[0].info.source()))
			continue
		}
		if prev.path != e.path {
			errs = append(errs, fmt.Errorf("routes %s and %s differ only by version but use different wildcard names; use the same names",
				prev.routes[0].info, e.routes[0].info))
			continue
		}
		prev.routes = append(prev.routes, e.routes...)
	}
	for _, e := range out {
		seen := map[string]*compiledRoute{}
		for _, r := range e.routes {
			if first, dup := seen[r.info.Version]; dup && len(e.routes) > 1 {
				errs = append(errs, fmt.Errorf("route %s is registered twice: %s and %s",
					r.info, first.info.source(), r.info.source()))
			}
			seen[r.info.Version] = r
		}
	}
	return out, errs
}

func register(mux *http.ServeMux, e *muxEntry, a *HTTPApp, v *VersioningOptions) error {
	p := e.host + e.path
	if e.method != "" {
		p = e.method + " " + p
	}
	var h http.Handler
	if len(e.routes) == 1 && (v == nil || v.kind == versionURI) {
		h = e.routes[0].handler
	} else {
		h = &versionDispatcher{routes: e.routes, opts: v, app: a}
	}
	if err := safeHandle(mux, p, h); err != nil {
		return fmt.Errorf("route %s (%s) conflicts with another route: %v", e.routes[0].info, e.routes[0].info.source(), err)
	}
	return nil
}

// safeHandle registers a pattern, turning ServeMux's panic on conflicting
// patterns into an error.
func safeHandle(mux *http.ServeMux, p string, h http.Handler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprint(r)
			// Drop ServeMux's "(registered at file:line)" noise, which points into Dygo.
			for {
				i := strings.Index(msg, " (registered at ")
				if i < 0 {
					break
				}
				j := strings.Index(msg[i:], ")")
				msg = msg[:i] + msg[i+j+1:]
			}
			err = fmt.Errorf("%s", strings.ReplaceAll(strings.TrimSpace(msg), "\n", " "))
		}
	}()
	mux.Handle(p, h)
	return nil
}

// versionDispatcher picks a route by the request's API version.
type versionDispatcher struct {
	routes []*compiledRoute
	opts   *VersioningOptions
	app    *HTTPApp
}

func (d *versionDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, want := range d.opts.requestVersions(r) {
		for _, rt := range d.routes {
			if rt.info.Version == want {
				rt.handler.ServeHTTP(w, r)
				return
			}
		}
	}
	for _, rt := range d.routes {
		if rt.info.Version == VersionNeutral {
			rt.handler.ServeHTTP(w, r)
			return
		}
	}
	d.app.notFound(w, r)
}

func (a *HTTPApp) notFound(w http.ResponseWriter, r *http.Request) {
	errs.WriteJSON(w, errs.ToBody(errs.NotFound(fmt.Sprintf("Cannot %s %s", r.Method, r.URL.Path))))
}

// ServeHTTP runs one request through the route's chain: recover → request
// scope → body limit → guards → interceptors → bind → pipes → handler → write.
// Errors and panics from any stage go to the filters, then the error writer.
func (rt *compiledRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a := rt.app
	st := &reqState{rt: rt, app: a}
	st.tw.ResponseWriter = w
	st.w = &st.tw
	// The request is not cloned with the new context here: typed handlers get
	// st.ctx directly, and st.request() clones only when something needs an
	// *http.Request whose Context() carries the state.
	ctx := context.WithValue(r.Context(), stateKey{}, st)
	st.r, st.ctx = r, ctx
	if a.c.hasRequestScope {
		defer func() {
			if st.scope != nil {
				st.scope.end()
			}
		}()
	}
	if rt.bodyLimit > 0 && r.Body != nil && r.Body != http.NoBody {
		r.Body = http.MaxBytesReader(st.w, r.Body, rt.bodyLimit)
	}
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			rt.fail(st, &PanicError{Value: p, Stack: debug.Stack()})
		}
	}()
	out, err := rt.run(st)
	if err == nil && !rt.ep.raw {
		err = writeOut(st, rt.rc, out)
	}
	if err != nil {
		rt.fail(st, err)
	}
}

// fail hands err to the route's filters, then to the error writer.
func (rt *compiledRoute) fail(st *reqState, err error) {
	if st.w.wrote {
		rt.app.c.log.ErrorContext(st.ctx, "dygo: error after the response started", "route", rt.info.String(), "err", err)
		return
	}
	for _, f := range rt.pl.filters {
		if f.Catch(err, st) {
			return
		}
	}
	rt.app.errWriter(st.w, st.request(), err)
}
