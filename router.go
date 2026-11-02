package dygo

import (
	"reflect"
	"slices"
)

// Controller is implemented by every type listed in a module's Controllers.
// Routes is called once at startup to register the controller's routes.
type Controller interface {
	Routes(r Router)
}

// Router registers routes. Prefix, Host, Version and Use return a child
// router and leave the receiver unchanged:
//
//	func (c *UsersController) Routes(r dygo.Router) {
//		r = r.Prefix("/users")
//		r.Get("/", dygo.Handle(c.List))
//		r.Get("/{id}", dygo.Handle(c.Get))
//	}
//
// Paths use Go's pattern syntax: {id} for a parameter, {path...} for the rest
// of the path. GET routes also answer HEAD.
type Router interface {
	// Prefix adds a path prefix for routes registered through the child.
	Prefix(p string) Router
	// Host restricts routes to a host, like "api.example.com" or "{tenant}.example.com".
	Host(h string) Router
	// Version sets the API versions of routes registered through the child.
	Version(v ...string) Router
	// Use applies options, such as middleware, to every route registered through the child.
	Use(opts ...RouteOption) Router

	Get(path string, h Handler, opts ...RouteOption)
	Post(path string, h Handler, opts ...RouteOption)
	Put(path string, h Handler, opts ...RouteOption)
	Patch(path string, h Handler, opts ...RouteOption)
	Delete(path string, h Handler, opts ...RouteOption)
	Head(path string, h Handler, opts ...RouteOption)
	Options(path string, h Handler, opts ...RouteOption)
	// All matches every method.
	All(path string, h Handler, opts ...RouteOption)
}

type ctrlInfo struct {
	typ reflect.Type
	mi  *moduleInfo
}

type routeDef struct {
	method   string
	host     string
	prefixes []string
	path     string
	versions []string
	handler  Handler
	ctrlOpts []RouteOption // from Router.Use: controller level
	opts     []RouteOption // the route's own: route level
	ctrl     ctrlInfo
}

type router struct {
	defs     *[]*routeDef
	ctrl     ctrlInfo
	prefixes []string
	host     string
	versions []string
	opts     []RouteOption
}

func (r *router) clone() *router {
	c := *r
	c.prefixes = slices.Clone(r.prefixes)
	c.opts = slices.Clone(r.opts)
	return &c
}

func (r *router) Prefix(p string) Router {
	c := r.clone()
	c.prefixes = append(c.prefixes, p)
	return c
}

func (r *router) Host(h string) Router {
	c := r.clone()
	c.host = h
	return c
}

func (r *router) Version(v ...string) Router {
	c := r.clone()
	c.versions = v
	return c
}

func (r *router) Use(opts ...RouteOption) Router {
	c := r.clone()
	c.opts = append(c.opts, opts...)
	return c
}

func (r *router) add(method, path string, h Handler, opts []RouteOption) {
	*r.defs = append(*r.defs, &routeDef{
		method: method, host: r.host, prefixes: slices.Clone(r.prefixes), path: path,
		versions: r.versions, handler: h, ctrlOpts: r.opts, opts: opts, ctrl: r.ctrl,
	})
}

func (r *router) Get(p string, h Handler, o ...RouteOption)     { r.add("GET", p, h, o) }
func (r *router) Post(p string, h Handler, o ...RouteOption)    { r.add("POST", p, h, o) }
func (r *router) Put(p string, h Handler, o ...RouteOption)     { r.add("PUT", p, h, o) }
func (r *router) Patch(p string, h Handler, o ...RouteOption)   { r.add("PATCH", p, h, o) }
func (r *router) Delete(p string, h Handler, o ...RouteOption)  { r.add("DELETE", p, h, o) }
func (r *router) Head(p string, h Handler, o ...RouteOption)    { r.add("HEAD", p, h, o) }
func (r *router) Options(p string, h Handler, o ...RouteOption) { r.add("OPTIONS", p, h, o) }
func (r *router) All(p string, h Handler, o ...RouteOption)     { r.add("", p, h, o) }

// RouteOption configures a route. Options given to Router.Use apply first, at
// controller level, then the route's own options, at route level.
type RouteOption interface {
	applyRoute(*routeConfig)
}

type routeOptionFunc func(*routeConfig)

func (f routeOptionFunc) applyRoute(rc *routeConfig) { f(rc) }

type routeConfig struct {
	code         int
	headers      [][2]string
	redirectURL  string
	redirectCode int
	middleware   []Middleware
	bodyLimit    *int64
	versions     []string
	strictJSON   bool

	level     int // levelController while applying Router.Use options, then levelRoute
	enhancers [kindCount][2][]any
	meta      [2]map[any]any
	merged    map[any]any // slice values set at both levels, joined at boot
}

// mergeMeta joins slice metadata set at both levels once, so that reading the
// merged value (meta.GetAllAndMerge) costs no allocation per request.
func (rc *routeConfig) mergeMeta() {
	for k, r := range rc.meta[levelRoute] {
		c, ok := rc.meta[levelController][k]
		if !ok {
			continue
		}
		cv, rv := reflect.ValueOf(c), reflect.ValueOf(r)
		if cv.Kind() != reflect.Slice || cv.Type() != rv.Type() {
			continue
		}
		// Exact capacity, so a caller's append copies instead of writing into it.
		m := reflect.MakeSlice(cv.Type(), 0, cv.Len()+rv.Len())
		m = reflect.AppendSlice(reflect.AppendSlice(m, cv), rv)
		if rc.merged == nil {
			rc.merged = map[any]any{}
		}
		rc.merged[k] = m.Interface()
	}
}

// HTTPCode sets the success status code. The default
// is 201 for POST and 200 otherwise. A handler can still change it per
// request with [SetStatus].
func HTTPCode(code int) RouteOption {
	return routeOptionFunc(func(rc *routeConfig) { rc.code = code })
}

// Header sets a static response header.
func Header(key, value string) RouteOption {
	return routeOptionFunc(func(rc *routeConfig) { rc.headers = append(rc.headers, [2]string{key, value}) })
}

// Redirect makes the route redirect after the handler succeeds. Return
// [RedirectTo] from the handler for a dynamic target.
func Redirect(url string, code int) RouteOption {
	return routeOptionFunc(func(rc *routeConfig) { rc.redirectURL, rc.redirectCode = url, code })
}

// RouteMiddleware adds net/http middleware to the route. With Router.Use
// it applies to every route of the controller.
func RouteMiddleware(mw ...Middleware) RouteOption {
	return routeOptionFunc(func(rc *routeConfig) { rc.middleware = append(rc.middleware, mw...) })
}

// BodyLimit overrides the application body size limit for the route. Zero or
// less means no limit.
func BodyLimit(n int64) RouteOption {
	return routeOptionFunc(func(rc *routeConfig) { rc.bodyLimit = &n })
}

// Version sets the route's API versions, overriding Router.Version and the
// default version. Use [VersionNeutral] for a route that matches every version.
func Version(v ...string) RouteOption {
	return routeOptionFunc(func(rc *routeConfig) { rc.versions = v })
}

// StrictJSON rejects JSON bodies that contain unknown fields.
func StrictJSON() RouteOption {
	return routeOptionFunc(func(rc *routeConfig) { rc.strictJSON = true })
}

// Options combines several route options into one:
//
//	func AdminOnly() dygo.RouteOption { return dygo.Options(authMiddleware, dygo.Header("Cache-Control", "no-store")) }
func Options(opts ...RouteOption) RouteOption {
	return routeOptionFunc(func(rc *routeConfig) {
		for _, o := range opts {
			o.applyRoute(rc)
		}
	})
}
