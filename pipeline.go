package dygo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"github.com/dygoframework/dygo/errs"
)

// The enhancer pipeline, in order:
//
//	Middleware → Guards → Interceptors (before) → Pipes (bind, then transform
//	and validate) → Handler → Interceptors (after) → write
//
// Exception filters receive any error or recovered panic from any stage.
// Enhancers bind at three levels: global (options or the App* groups), the
// controller (Router.Use) and the route (route options). Guards, interceptors
// and pipes run global first, then controller, then route; filters run route
// first, then controller, then global.

// ContextKind names the transport an [ArgumentsHost] belongs to.
type ContextKind string

// The transports. KindHTTP is a controller route; KindRPC a microservice
// pattern (package micro); KindWS a WebSocket gateway message (package ws);
// KindGraphQL a query, mutation or subscription field (package graphql).
const (
	KindHTTP    ContextKind = "http"
	KindRPC     ContextKind = "rpc"
	KindWS      ContextKind = "ws"
	KindGraphQL ContextKind = "graphql"
)

// HTTPHost gives access to the HTTP request and response.
type HTTPHost interface {
	Request() *http.Request
	Response() http.ResponseWriter
}

// ArgumentsHost is what an exception filter receives.
type ArgumentsHost interface {
	// Kind is the transport: KindHTTP, KindRPC or KindWS.
	Kind() ContextKind
	// HTTP returns the request and response. It is nil unless Kind is KindHTTP.
	HTTP() HTTPHost
	// RPC returns the message of a microservice handler. It is nil unless
	// Kind is KindRPC.
	RPC() RPCHost
	// WS returns the client and message of a WebSocket gateway handler. It is
	// nil unless Kind is KindWS.
	WS() WSHost
	// GraphQL returns the field being resolved. It is nil unless Kind is
	// KindGraphQL.
	GraphQL() GraphQLHost
	// Context is the request context.
	Context() context.Context
}

// ExecutionContext describes the request being handled, for guards,
// interceptors and pipes.
//
// It is not itself a context.Context: call Context to pass the
// request context on, and SetContext to change it for later stages and the
// handler (for example to add the authenticated user).
type ExecutionContext interface {
	ArgumentsHost
	// SetContext replaces the request context seen by later stages and the
	// handler. ctx must be derived from Context().
	SetContext(ctx context.Context)
	// Route describes the matched route.
	Route() RouteInfo
	// Controller is the type of the controller that declared the route.
	Controller() reflect.Type
	// RouteMetadata returns the value set with [SetMetadata] on the route.
	RouteMetadata(key any) (any, bool)
	// ControllerMetadata returns the value set with [SetMetadata] through
	// Router.Use, that is for the whole controller.
	ControllerMetadata(key any) (any, bool)
	// MergedMetadata returns the controller's and the route's values joined,
	// controller first, when both are slices of the same type (joined once at
	// startup; do not modify it). Otherwise it returns the route's value, or
	// else the controller's.
	MergedMetadata(key any) (any, bool)
}

// Guard decides whether a request may go on. Returning
// false gives a 403; returning an error sends that error to the filters, so a
// guard can return errs.Unauthorized for a 401.
type Guard interface {
	CanActivate(ec ExecutionContext) (bool, error)
}

// GuardFunc is a function used as a [Guard].
type GuardFunc func(ec ExecutionContext) (bool, error)

// CanActivate calls f.
func (f GuardFunc) CanActivate(ec ExecutionContext) (bool, error) { return f(ec) }

// CallHandler runs the rest of the pipeline: later interceptors, pipes and the
// handler. Handle may be called more than once, for a retry.
type CallHandler interface {
	Handle() (any, error)
}

// Interceptor wraps the handler. Work before
// next.Handle runs before the handler; the result can be changed, replaced or
// mapped to another error afterwards:
//
//	func (Envelope) Intercept(ec dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
//		out, err := next.Handle()
//		if err != nil {
//			return nil, err
//		}
//		return map[string]any{"data": out}, nil
//	}
type Interceptor interface {
	Intercept(ec ExecutionContext, next CallHandler) (any, error)
}

// InterceptorFunc is a function used as an [Interceptor].
type InterceptorFunc func(ec ExecutionContext, next CallHandler) (any, error)

// Intercept calls f.
func (f InterceptorFunc) Intercept(ec ExecutionContext, next CallHandler) (any, error) {
	return f(ec, next)
}

// Pipe transforms or validates a handler's input after binding. in is a
// pointer to the bound input struct (the In of [Handle]), so a pipe can
// normalise fields in place. An error stops the request; return a
// *validate.ValidationError or an *errs.HTTPError for a 400. Pipes do not run
// for [HandleRaw] routes.
type Pipe interface {
	Transform(ec ExecutionContext, in any) error
}

// PipeFunc is a function used as a [Pipe].
type PipeFunc func(ec ExecutionContext, in any) error

// Transform calls f.
func (f PipeFunc) Transform(ec ExecutionContext, in any) error { return f(ec, in) }

// ExceptionFilter handles errors. Catch returns true when
// it wrote the response; false passes the error to the next filter and in the
// end to the error writer.
type ExceptionFilter interface {
	Catch(err error, host ArgumentsHost) bool
}

// FilterFunc is a function used as an [ExceptionFilter].
type FilterFunc func(err error, host ArgumentsHost) bool

// Catch calls f.
func (f FilterFunc) Catch(err error, host ArgumentsHost) bool { return f(err, host) }

// FilterFor returns a filter for errors of type E, matched with errors.As,
// like @Catch(SomeException):
//
//	dygo.FilterFor(func(e *store.ConflictError, host dygo.ArgumentsHost) {
//		errs.WriteJSON(host.HTTP().Response(), errs.ToBody(errs.Conflict(e.Error())))
//	})
func FilterFor[E error](fn func(e E, host ArgumentsHost)) ExceptionFilter {
	return FilterFunc(func(err error, host ArgumentsHost) bool {
		var target E
		if !errors.As(err, &target) {
			return false
		}
		fn(target, host)
		return true
	})
}

// PanicError is the error filters receive for a recovered panic. The default
// error writer answers 500 and logs it with the stack.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("panic: %v\n%s", e.Value, e.Stack) }

// Group names that make providers global enhancers with dependency injection:
//
//	dygo.Provide(NewAuthGuard, dygo.Group(dygo.AppGuards))
//
// Members must be singletons. They run after the enhancers given with the
// WithGlobal* options.
const (
	AppGuards       = "dygo.app.guards"
	AppInterceptors = "dygo.app.interceptors"
	AppPipes        = "dygo.app.pipes"
	AppFilters      = "dygo.app.filters"
)

// Enhancer arguments. Guards, Interceptors, Pipes, Filters and the WithGlobal*
// options accept, for each enhancer:
//
//   - an instance (or a matching func, which is converted to GuardFunc and so on);
//   - [Ref], to use a provider visible from the controller's module;
//   - a constructor, such as NewAuthGuard, called once per module with its
//     parameters injected from that module (like listing a class in
//     @UseGuards). The result gets no lifecycle hooks; register it as a
//     provider and use Ref when it needs them.
//
// Everything is resolved at startup, so a missing dependency is a boot error.

// Ref refers to the provider registered under T, as an enhancer argument:
//
//	r = r.Use(dygo.Guards(dygo.Ref[*auth.Guard]()))
func Ref[T any]() any { return refSpec{tok: TypeOf[T]()} }

// RefToken is [Ref] for a named token.
func RefToken(tok Token) any { return refSpec{tok: tok} }

type refSpec struct{ tok Token }

type enhancerKind int

const (
	kindGuard enhancerKind = iota
	kindInterceptor
	kindPipe
	kindFilter
	kindCount
)

const (
	levelController = 0
	levelRoute      = 1
)

func enhancerOption(k enhancerKind, specs []any) RouteOption {
	return routeOptionFunc(func(rc *routeConfig) {
		rc.enhancers[k][rc.level] = append(rc.enhancers[k][rc.level], specs...)
	})
}

// Guards adds guards, like @UseGuards. On Router.Use they guard every route of
// the controller.
func Guards(g ...any) RouteOption { return enhancerOption(kindGuard, g) }

// Interceptors adds interceptors, like @UseInterceptors.
func Interceptors(i ...any) RouteOption { return enhancerOption(kindInterceptor, i) }

// Pipes adds pipes, like @UsePipes.
func Pipes(p ...any) RouteOption { return enhancerOption(kindPipe, p) }

// Filters adds exception filters, like @UseFilters.
func Filters(f ...any) RouteOption { return enhancerOption(kindFilter, f) }

// SetMetadata attaches a value to the route, or to every route of the
// controller when given to Router.Use. Guards and
// interceptors read it from the [ExecutionContext]; package meta adds typed keys.
// Use a key of an unexported type to avoid collisions.
func SetMetadata(key, value any) RouteOption {
	return routeOptionFunc(func(rc *routeConfig) {
		if rc.meta[rc.level] == nil {
			rc.meta[rc.level] = map[any]any{}
		}
		rc.meta[rc.level][key] = value
	})
}

// WithGlobalGuards adds guards to every route, like app.useGlobalGuards.
// Constructors and Refs resolve from the root module.
func WithGlobalGuards(g ...any) Option {
	return func(o *options) { o.http.global[kindGuard] = append(o.http.global[kindGuard], g...) }
}

// WithGlobalInterceptors adds interceptors to every route.
func WithGlobalInterceptors(i ...any) Option {
	return func(o *options) { o.http.global[kindInterceptor] = append(o.http.global[kindInterceptor], i...) }
}

// WithGlobalPipes adds pipes to every typed route.
func WithGlobalPipes(p ...any) Option {
	return func(o *options) { o.http.global[kindPipe] = append(o.http.global[kindPipe], p...) }
}

// WithGlobalFilters adds exception filters to every route. They run after
// route and controller filters, before the error writer.
func WithGlobalFilters(f ...any) Option {
	return func(o *options) { o.http.global[kindFilter] = append(o.http.global[kindFilter], f...) }
}

var errForbidden = errs.Forbidden("Forbidden resource")

// pipeline holds a route's resolved enhancers.
type pipeline struct {
	guards       []Guard
	interceptors []Interceptor
	pipes        []Pipe
	filters      []ExceptionFilter
}

// enhancerResolver turns enhancer arguments into instances at startup.
type enhancerResolver struct {
	a      *App
	ctors  map[ctorKey]any
	global pipeline
}

type ctorKey struct {
	mi *moduleInfo
	fn uintptr
}

// newEnhancerResolver resolves the global enhancers: the specs in g and, when
// groups is true, the members of the App* groups.
func newEnhancerResolver(a *App, g *[kindCount][]any, groups bool) (*enhancerResolver, []error) {
	er := &enhancerResolver{a: a, ctors: map[ctorKey]any{}}
	root := a.c.root
	group := func(name string) string {
		if groups {
			return name
		}
		return ""
	}
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("global enhancers: %w", err))
		}
	}
	var err error
	er.global.guards, err = resolveAll[Guard](er, root, g[kindGuard], group(AppGuards))
	collect(err)
	er.global.interceptors, err = resolveAll[Interceptor](er, root, g[kindInterceptor], group(AppInterceptors))
	collect(err)
	er.global.pipes, err = resolveAll[Pipe](er, root, g[kindPipe], group(AppPipes))
	collect(err)
	er.global.filters, err = resolveAll[ExceptionFilter](er, root, g[kindFilter], group(AppFilters))
	collect(err)
	return er, errs
}

// route builds the pipeline of one route from its controller's module.
func (er *enhancerResolver) route(mi *moduleInfo, rc *routeConfig) (pipeline, error) {
	var p pipeline
	var es []error
	add := func(err error) {
		if err != nil {
			es = append(es, err)
		}
	}
	ctrlG, err := resolveAll[Guard](er, mi, rc.enhancers[kindGuard][levelController], "")
	add(err)
	routeG, err := resolveAll[Guard](er, mi, rc.enhancers[kindGuard][levelRoute], "")
	add(err)
	ctrlI, err := resolveAll[Interceptor](er, mi, rc.enhancers[kindInterceptor][levelController], "")
	add(err)
	routeI, err := resolveAll[Interceptor](er, mi, rc.enhancers[kindInterceptor][levelRoute], "")
	add(err)
	ctrlP, err := resolveAll[Pipe](er, mi, rc.enhancers[kindPipe][levelController], "")
	add(err)
	routeP, err := resolveAll[Pipe](er, mi, rc.enhancers[kindPipe][levelRoute], "")
	add(err)
	ctrlF, err := resolveAll[ExceptionFilter](er, mi, rc.enhancers[kindFilter][levelController], "")
	add(err)
	routeF, err := resolveAll[ExceptionFilter](er, mi, rc.enhancers[kindFilter][levelRoute], "")
	add(err)
	if len(es) > 0 {
		return p, errors.Join(es...)
	}
	p.guards = concat(er.global.guards, ctrlG, routeG)
	p.interceptors = concat(er.global.interceptors, ctrlI, routeI)
	p.pipes = concat(er.global.pipes, ctrlP, routeP)
	p.filters = concat(routeF, ctrlF, er.global.filters)
	return p, nil
}

// concat joins slices and returns nil when all are empty, so routes without
// enhancers keep the fast path.
func concat[T any](parts ...[]T) []T {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	if n == 0 {
		return nil
	}
	out := make([]T, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func resolveAll[T any](er *enhancerResolver, mi *moduleInfo, specs []any, group string) ([]T, error) {
	var out []T
	var es []error
	for _, s := range specs {
		v, err := resolveOne[T](er, mi, s)
		if err != nil {
			es = append(es, err)
			continue
		}
		out = append(out, v)
	}
	if group != "" {
		for _, n := range er.a.c.groups[group] {
			inst, err := er.a.c.instanceOf(n)
			if err != nil {
				es = append(es, fmt.Errorf("group %q member %s: %w", group, n, err))
				continue
			}
			v, ok := inst.(T)
			if !ok {
				es = append(es, fmt.Errorf("group %q member %s is %T, which is not a %s", group, n, inst, reflect.TypeFor[T]()))
				continue
			}
			out = append(out, v)
		}
	}
	return out, errors.Join(es...)
}

func resolveOne[T any](er *enhancerResolver, mi *moduleInfo, spec any) (T, error) {
	var zero T
	want := reflect.TypeFor[T]()
	switch s := spec.(type) {
	case nil:
		return zero, fmt.Errorf("%s is nil", want)
	case T:
		return s, nil
	case refSpec:
		inst, err := mi.ref.Get(s.tok)
		if err != nil {
			return zero, fmt.Errorf("%s from module %q: %w", want, mi.name, err)
		}
		v, ok := inst.(T)
		if !ok {
			return zero, fmt.Errorf("%s is %T, which is not a %s", s.tok, inst, want)
		}
		return v, nil
	}
	rv := reflect.ValueOf(spec)
	if rv.Kind() != reflect.Func {
		return zero, fmt.Errorf("%T is not a %s, a dygo.Ref or a constructor", spec, want)
	}
	if ft := funcAdapter(want); ft != nil && rv.Type().ConvertibleTo(ft) {
		return rv.Convert(ft).Interface().(T), nil
	}
	key := ctorKey{mi: mi, fn: rv.Pointer()}
	inst, ok := er.ctors[key]
	if !ok {
		var err error
		inst, err = mi.ref.Create(spec)
		if err != nil {
			return zero, fmt.Errorf("constructing %s with %s in module %q: %w", want, funcName(rv), mi.name, err)
		}
		er.ctors[key] = inst
	}
	v, ok := inst.(T)
	if !ok {
		return zero, fmt.Errorf("%s returned %T, which is not a %s", funcName(rv), inst, want)
	}
	return v, nil
}

// funcAdapter returns the func type that implements interface t, so plain
// functions can be passed without converting them to GuardFunc and the like.
func funcAdapter(t reflect.Type) reflect.Type {
	switch t {
	case reflect.TypeFor[Guard]():
		return reflect.TypeFor[GuardFunc]()
	case reflect.TypeFor[Interceptor]():
		return reflect.TypeFor[InterceptorFunc]()
	case reflect.TypeFor[Pipe]():
		return reflect.TypeFor[PipeFunc]()
	case reflect.TypeFor[ExceptionFilter]():
		return reflect.TypeFor[FilterFunc]()
	}
	return nil
}

// run executes guards, then the interceptor chain around pipes and the handler.
func (rt *compiledRoute) run(st *reqState) (any, error) {
	for _, g := range rt.pl.guards {
		ok, err := g.CanActivate(st)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errForbidden
		}
	}
	return rt.intercept(st, 0)
}

func (rt *compiledRoute) intercept(st *reqState, i int) (any, error) {
	if i == len(rt.pl.interceptors) {
		return rt.ep.call(st)
	}
	return rt.pl.interceptors[i].Intercept(st, st.next(i))
}

// nextStep is the CallHandler given to interceptor i. reqState holds a few of
// them inline, so interceptors cost no allocation.
type nextStep struct {
	st *reqState
	i  int
}

func (n *nextStep) Handle() (any, error) { return n.st.rt.intercept(n.st, n.i+1) }

func (st *reqState) next(i int) CallHandler {
	if i < len(st.nexts) {
		n := &st.nexts[i]
		n.st, n.i = st, i
		return n
	}
	return &nextStep{st: st, i: i}
}

// ExecutionContext and ArgumentsHost, implemented by the request state.

func (st *reqState) Kind() ContextKind               { return KindHTTP }
func (st *reqState) HTTP() HTTPHost                  { return st }
func (st *reqState) RPC() RPCHost                    { return nil }
func (st *reqState) WS() WSHost                      { return nil }
func (st *reqState) GraphQL() GraphQLHost            { return nil }
func (st *reqState) Context() context.Context        { return st.ctx }
func (st *reqState) Request() *http.Request          { return st.request() }
func (st *reqState) Response() http.ResponseWriter   { return st.w }
func (st *reqState) Route() RouteInfo                { return st.rt.info }
func (st *reqState) Controller() reflect.Type        { return st.rt.ctrl }
func (st *reqState) RouteMetadata(k any) (any, bool) { return lookupMeta(st.rt.rc.meta[levelRoute], k) }
func (st *reqState) ControllerMetadata(k any) (any, bool) {
	return lookupMeta(st.rt.rc.meta[levelController], k)
}

func (st *reqState) SetContext(ctx context.Context) {
	if ctx == nil || ctx == st.ctx {
		return
	}
	st.ctx = ctx
}

func (st *reqState) MergedMetadata(k any) (any, bool) { return st.rt.rc.mergedMetadata(k) }

func (rc *routeConfig) mergedMetadata(k any) (any, bool) {
	if v, ok := rc.merged[k]; ok {
		return v, true
	}
	if v, ok := rc.meta[levelRoute][k]; ok {
		return v, true
	}
	return lookupMeta(rc.meta[levelController], k)
}

func lookupMeta(m map[any]any, k any) (any, bool) {
	v, ok := m[k]
	return v, ok
}
