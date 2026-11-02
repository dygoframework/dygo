package dygo

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dygoframework/dygo/internal/binder"
)

// Handler is a compiled route handler. Create one with [Handle],
// [HandleNoInput] or [HandleRaw].
type Handler interface {
	endpoint(rc *routeConfig, pathVars []string, pipes []Pipe) (*endpoint, error)
	name() string
	types() (in, out reflect.Type)
}

// endpoint is a handler ready to serve. call returns the value to write; raw
// endpoints write the response themselves and return nil.
type endpoint struct {
	call func(st *reqState) (any, error)
	raw  bool
}

type typedHandler[In, Out any] struct {
	fn     func(context.Context, In) (Out, error)
	fnName string
}

// Handle turns a typed function into a route handler. In is a struct whose
// tagged fields are filled from the request:
//
//	type GetUserIn struct {
//		ID     uuid.UUID `path:"id"`
//		Expand bool      `query:"expand"`
//		Tenant string    `header:"X-Tenant" required:"true"`
//		Body   CreateDTO `body:"json"`
//	}
//
// Tags: path, query, header, cookie, form, file, body ("json", "form" or
// "raw"), plus default:"..." and required:"true". Out is written as JSON
// unless it is a [Responder], [NoContent], string or []byte. A returned error
// is written by the error writer; see package errs.
//
// The binding plan is compiled when routes are built, so a bad In type is a
// startup error.
func Handle[In, Out any](fn func(context.Context, In) (Out, error)) Handler {
	return &typedHandler[In, Out]{fn: fn, fnName: handlerName(fn)}
}

// HandleNoInput is Handle for a handler that needs nothing from the request.
func HandleNoInput[Out any](fn func(context.Context) (Out, error)) Handler {
	return &typedHandler[struct{}, Out]{
		fn:     func(ctx context.Context, _ struct{}) (Out, error) { return fn(ctx) },
		fnName: handlerName(fn),
	}
}

func (h *typedHandler[In, Out]) name() string { return h.fnName }

func (h *typedHandler[In, Out]) types() (in, out reflect.Type) {
	return reflect.TypeFor[In](), reflect.TypeFor[Out]()
}

func (h *typedHandler[In, Out]) endpoint(rc *routeConfig, pathVars []string, pipes []Pipe) (*endpoint, error) {
	plan, err := binder.Compile(reflect.TypeFor[In]())
	if err != nil {
		return nil, err
	}
	for _, n := range plan.PathNames() {
		if !slices.Contains(pathVars, n) {
			return nil, fmt.Errorf("input field tagged path:%q has no {%s} in the route path", n, n)
		}
	}
	opts := binder.Options{StrictJSON: rc.strictJSON}
	custom := plan.UsesCustomSources()
	return &endpoint{call: func(st *reqState) (any, error) {
		in := new(In)
		r := st.r
		if custom {
			r = st.request()
		}
		if err := plan.Bind(r, reflect.ValueOf(in).Elem(), opts); err != nil {
			return nil, err
		}
		for _, p := range pipes {
			if err := p.Transform(st, in); err != nil {
				return nil, err
			}
		}
		return h.fn(st.ctx, *in)
	}}, nil
}

type rawHandler struct {
	fn     func(http.ResponseWriter, *http.Request) error
	fnName string
}

// HandleRaw is the escape hatch: fn gets the plain ResponseWriter and Request.
// An error it returns is written by the error writer if nothing was written yet.
func HandleRaw(fn func(w http.ResponseWriter, r *http.Request) error) Handler {
	return &rawHandler{fn: fn, fnName: handlerName(fn)}
}

func (h *rawHandler) name() string { return h.fnName }

func (h *rawHandler) types() (in, out reflect.Type) { return nil, nil }

func (h *rawHandler) endpoint(*routeConfig, []string, []Pipe) (*endpoint, error) {
	return &endpoint{raw: true, call: func(st *reqState) (any, error) {
		return nil, h.fn(st.w, st.request())
	}}, nil
}

func handlerName(fn any) string {
	v := reflect.ValueOf(fn)
	if v.Kind() != reflect.Func || v.IsNil() {
		return "<nil handler>"
	}
	return strings.TrimSuffix(funcName(v), "-fm")
}

// reqState is the per-request state Dygo keeps in the request context.
//
// It is allocated once per request and holds the response writer wrapper and
// the lazily created request scope, so neither needs its own allocation.
type reqState struct {
	tw      trackWriter
	w       *trackWriter  // &tw
	r       *http.Request // as received; its Context() lacks the state, see request()
	ctx     context.Context
	withCtx atomic.Pointer[http.Request]
	rt      *compiledRoute
	status  int
	app     *HTTPApp
	nexts   [4]nextStep // CallHandlers for the first interceptors

	scopeOnce sync.Once
	scope     *requestScope
}

// requestScope returns the request's scope, creating it on first use. Requests
// that never touch a request scoped provider pay nothing for it.
func (st *reqState) requestScope() *requestScope {
	st.scopeOnce.Do(func() {
		st.scope = &requestScope{ctx: st.ctx, req: st.request(), instances: map[*node]reflect.Value{}}
		if st.rt != nil && st.rt.app.c.strategy != nil {
			st.scope.cid.ID, st.scope.cid.Payload = st.rt.app.c.strategy(st.scope.req)
		}
	})
	return st.scope
}

type stateKey struct{}

func stateFrom(ctx context.Context) *reqState {
	st, _ := ctx.Value(stateKey{}).(*reqState)
	return st
}

// Request returns the *http.Request of the request handled with ctx, or nil.
func Request(ctx context.Context) *http.Request {
	if st := stateFrom(ctx); st != nil {
		return st.request()
	}
	return nil
}

// request returns the request with Context() == st.ctx, cloning it only when
// the context changed since the last clone. The clone is kept in an atomic
// pointer because handlers may call dygo.Request from several goroutines.
func (st *reqState) request() *http.Request {
	if r := st.withCtx.Load(); r != nil && r.Context() == st.ctx {
		return r
	}
	r := st.r.WithContext(st.ctx)
	st.withCtx.Store(r)
	return r
}

// SetStatus overrides the success status code from inside a handler. To fix
// it for the whole route, use the [HTTPCode] route option.
func SetStatus(ctx context.Context, code int) {
	if st := stateFrom(ctx); st != nil {
		st.status = code
	}
}

// ResponseStatus returns the status set with SetStatus during this request, or
// 0. Interceptors use it to see the real status of a successful response,
// for example a health check that answers 503.
func ResponseStatus(ctx context.Context) int {
	if st := stateFrom(ctx); st != nil {
		return st.status
	}
	return 0
}

// SetHeader sets a response header from inside a handler.
func SetHeader(ctx context.Context, key, value string) {
	if st := stateFrom(ctx); st != nil {
		st.w.Header().Set(key, value)
	}
}

// SetCookie adds a Set-Cookie header from inside a handler.
func SetCookie(ctx context.Context, c *http.Cookie) {
	if st := stateFrom(ctx); st != nil {
		http.SetCookie(st.w, c)
	}
}

// RouteOf describes the route handling ctx. The zero RouteInfo is returned
// outside a request.
func RouteOf(ctx context.Context) RouteInfo {
	if st := stateFrom(ctx); st != nil {
		return st.rt.info
	}
	return RouteInfo{}
}

// RoutesOf returns the application's route table from inside a request, for
// handlers that describe the API, such as package openapi. It returns nil
// outside a request.
func RoutesOf(ctx context.Context) []RouteInfo {
	if st := stateFrom(ctx); st != nil && st.app != nil {
		return st.app.routes
	}
	return nil
}

// Stopping returns a channel that is closed when the server starts shutting
// down. Long running handlers, such as an SSE stream waiting for events, should
// select on it so that shutdown does not wait for the shutdown timeout. It
// returns nil (a channel that never closes) outside a request.
func Stopping(ctx context.Context) <-chan struct{} {
	if st := stateFrom(ctx); st != nil && st.app != nil {
		return st.app.stopping
	}
	return nil
}

// trackWriter records whether the response has started, so an error after a
// partial write is logged instead of corrupting the response.
type trackWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *trackWriter) WriteHeader(code int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *trackWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush and deadlines.
func (w *trackWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush implements http.Flusher, so code that type-asserts the writer (SSE
// helpers, proxies) keeps working inside HandleRaw.
func (w *trackWriter) Flush() {
	w.wrote = true
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// ReadFrom keeps sendfile working when a file is copied to the response
// (dygo.File, http.ServeContent).
func (w *trackWriter) ReadFrom(src io.Reader) (int64, error) {
	w.wrote = true
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(struct{ io.Writer }{w.ResponseWriter}, src)
}

// Hijack implements http.Hijacker, so WebSocket libraries work inside HandleRaw.
func (w *trackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.wrote = true
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
