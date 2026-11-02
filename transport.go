package dygo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"runtime/debug"
	"sync"
)

// Transports other than HTTP (package micro and its brokers, package ws) run
// their handlers through the same guards, interceptors, pipes and filters as
// controller routes. They build a [Pipeline] per handler at startup with
// [NewEnhancers], and are started and stopped with the application through
// [App.ConnectMicroservice].

// RPCHost gives access to a microservice message.
type RPCHost interface {
	// Pattern is the pattern the handler was registered for.
	Pattern() string
	// Data is the decoded payload, a pointer to the handler's input.
	Data() any
	// Message is the transport's own message, for example a *nats.Msg.
	Message() any
}

// WSHost gives access to a WebSocket message.
type WSHost interface {
	// Pattern is the event name the handler was registered for.
	Pattern() string
	// Data is the decoded payload, a pointer to the handler's input.
	Data() any
	// Client is the connection that sent the message, a *ws.Client.
	Client() any
}

// GraphQLHost gives access to the GraphQL field being resolved.
type GraphQLHost interface {
	// Field is the root field as "Type.field", such as "Query.user".
	Field() string
	// Args are the field's arguments.
	Args() map[string]any
	// Request is the HTTP request that carried the operation, nil for a
	// subscription over a WebSocket after the handshake.
	Request() *http.Request
}

// ErrFiltered is returned by [Pipeline.Run] when an exception filter caught
// the error. The filter has answered (or chosen not to), so the transport
// sends nothing more.
var ErrFiltered = errors.New("dygo: error handled by an exception filter")

// Endpoint describes one transport handler for [Enhancers.Pipeline].
type Endpoint struct {
	// Kind is KindRPC, KindWS or KindGraphQL. For KindGraphQL, the msg given
	// to Run must implement [GraphQLHost].
	Kind ContextKind
	// Pattern is the subject, method or event the handler serves.
	Pattern string
	// Owner is the provider that declared the handler. Refs and enhancer
	// constructors resolve from its module.
	Owner any
	// Handler is the handler function name, for errors and [RouteInfo].
	Handler string
	// Input and Output are the handler's types, nil when it has none.
	Input, Output reflect.Type
	// ControllerOptions apply to every handler of the owner, like Router.Use;
	// Options to this handler only. Only enhancers and metadata are used.
	ControllerOptions, Options []RouteOption
}

// Enhancers resolves the enhancers of one transport's handlers.
type Enhancers struct {
	mu sync.Mutex
	a  *App
	er *enhancerResolver
}

// NewEnhancers prepares pipelines for a transport. global holds the
// transport's own global enhancers (dygo.Guards(...) and so on). With
// inheritApp the app wide ones apply too: the WithGlobal* options and the
// App* groups, for hybrid apps. Without it a
// transport does not run the HTTP app's global enhancers, which often read
// HTTPHost.
func NewEnhancers(a *App, global []RouteOption, inheritApp bool) (*Enhancers, error) {
	rc := &routeConfig{level: levelRoute}
	for _, o := range global {
		if o != nil {
			o.applyRoute(rc)
		}
	}
	var g [kindCount][]any
	for k := range g {
		if inheritApp {
			g[k] = append(g[k], a.opts.http.global[k]...)
		}
		g[k] = append(g[k], rc.enhancers[k][levelRoute]...)
	}
	er, errs := newEnhancerResolver(a, &g, inheritApp)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &Enhancers{a: a, er: er}, nil
}

// Pipeline resolves the enhancers of one handler.
func (e *Enhancers) Pipeline(ep Endpoint) (*Pipeline, error) {
	rc := &routeConfig{level: levelController}
	for _, o := range ep.ControllerOptions {
		if o != nil {
			o.applyRoute(rc)
		}
	}
	rc.level = levelRoute
	for _, o := range ep.Options {
		if o != nil {
			o.applyRoute(rc)
		}
	}
	rc.mergeMeta()
	mi := e.a.moduleOf(ep.Owner)
	info := RouteInfo{
		Method: string(ep.Kind), Path: ep.Pattern, Module: mi.name, Handler: ep.Handler,
		Input: ep.Input, Output: ep.Output, rc: rc,
	}
	var ctrl reflect.Type
	if ep.Owner != nil {
		ctrl = reflect.TypeOf(ep.Owner)
		info.Controller = ctrl.String()
	}
	e.mu.Lock()
	pl, err := e.er.route(mi, rc)
	e.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("%s %q (%s): %w", ep.Kind, ep.Pattern, info.source(), err)
	}
	return &Pipeline{pl: pl, info: info, ctrl: ctrl, kind: ep.Kind}, nil
}

// moduleOf returns the module whose provider built inst, or the root module.
func (a *App) moduleOf(inst any) *moduleInfo {
	if inst != nil && reflect.TypeOf(inst).Comparable() {
		for _, n := range a.c.built {
			if n.instance.Comparable() && n.instance.Interface() == inst {
				return n.mod
			}
		}
	}
	return a.c.root
}

// Pipeline is the resolved guards, interceptors, pipes and filters of one
// transport handler.
type Pipeline struct {
	pl   pipeline
	info RouteInfo
	ctrl reflect.Type
	kind ContextKind
}

// Info describes the handler. Method is the kind ("rpc" or "ws") and Path the
// pattern.
func (p *Pipeline) Info() RouteInfo { return p.info }

// Run handles one message: guards, then interceptors around pipes and fn.
// in is the decoded input (a pointer, given to pipes and as Data), msg the
// transport's message or client. An error, or a panic recovered as a
// [*PanicError], goes to the filters; Run returns [ErrFiltered] when one of
// them caught it and the error otherwise, for the transport to answer.
func (p *Pipeline) Run(ctx context.Context, msg, in any, fn func(ctx context.Context) (any, error)) (out any, err error) {
	st := &msgState{p: p, ctx: ctx, msg: msg, in: in, fn: fn}
	defer func() {
		if v := recover(); v != nil {
			out, err = nil, &PanicError{Value: v, Stack: debug.Stack()}
		}
		if err != nil {
			err = p.catch(st, err)
		}
	}()
	for _, g := range p.pl.guards {
		ok, err := g.CanActivate(st)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errForbidden
		}
	}
	return st.intercept(0)
}

// Fail sends an error that happened before Run, such as a payload that
// does not decode, through the filters. It returns ErrFiltered or err.
func (p *Pipeline) Fail(ctx context.Context, msg any, err error) error {
	return p.catch(&msgState{p: p, ctx: ctx, msg: msg}, err)
}

func (p *Pipeline) catch(st *msgState, err error) error {
	for _, f := range p.pl.filters {
		if f.Catch(err, st) {
			return ErrFiltered
		}
	}
	return err
}

// msgState is the ExecutionContext of a transport message.
type msgState struct {
	p   *Pipeline
	ctx context.Context
	msg any
	in  any
	fn  func(ctx context.Context) (any, error)
}

type msgNext struct {
	st *msgState
	i  int
}

func (n msgNext) Handle() (any, error) { return n.st.intercept(n.i + 1) }

func (st *msgState) intercept(i int) (any, error) {
	if i == len(st.p.pl.interceptors) {
		if st.in != nil {
			for _, pp := range st.p.pl.pipes {
				if err := pp.Transform(st, st.in); err != nil {
					return nil, err
				}
			}
		}
		return st.fn(st.ctx)
	}
	return st.p.pl.interceptors[i].Intercept(st, msgNext{st: st, i: i})
}

func (st *msgState) Kind() ContextKind        { return st.p.kind }
func (st *msgState) HTTP() HTTPHost           { return nil }
func (st *msgState) Context() context.Context { return st.ctx }
func (st *msgState) Route() RouteInfo         { return st.p.info }
func (st *msgState) Controller() reflect.Type { return st.p.ctrl }
func (st *msgState) Pattern() string          { return st.p.info.Path }
func (st *msgState) Data() any                { return st.in }
func (st *msgState) Message() any             { return st.msg }
func (st *msgState) Client() any              { return st.msg }

func (st *msgState) RPC() RPCHost {
	if st.p.kind == KindRPC {
		return st
	}
	return nil
}

func (st *msgState) WS() WSHost {
	if st.p.kind == KindWS {
		return st
	}
	return nil
}

func (st *msgState) GraphQL() GraphQLHost {
	if h, ok := st.msg.(GraphQLHost); ok && st.p.kind == KindGraphQL {
		return h
	}
	return nil
}

func (st *msgState) SetContext(ctx context.Context) {
	if ctx != nil {
		st.ctx = ctx
	}
}

func (st *msgState) RouteMetadata(k any) (any, bool) {
	return lookupMeta(st.p.info.rc.meta[levelRoute], k)
}

func (st *msgState) ControllerMetadata(k any) (any, bool) {
	return lookupMeta(st.p.info.rc.meta[levelController], k)
}

func (st *msgState) MergedMetadata(k any) (any, bool) { return st.p.info.rc.mergedMetadata(k) }

// Transport is a server other than the HTTP one: a message broker consumer,
// a gRPC server. Packages such as micro/nats implement it.
type Transport interface {
	// Listen finds its handlers in a (usually with [ProvidersImplementing]),
	// builds their pipelines and starts serving. It returns once the
	// transport takes messages; a wiring problem is returned as an error.
	Listen(ctx context.Context, a *App) error
	// Close stops taking messages and waits for running handlers until ctx
	// is done.
	Close(ctx context.Context) error
}

// ConnectMicroservice adds a transport to the application, for a hybrid app.
// [HTTPApp.Listen] and [App.Run]
// start it with [App.StartAllMicroservices] if that was not called;
// shutdown closes it after BeforeApplicationShutdown, together with the HTTP
// drain.
func (a *App) ConnectMicroservice(t Transport) {
	a.closeMu.Lock()
	defer a.closeMu.Unlock()
	a.transports = append(a.transports, &transportState{t: t})
}

// StartAllMicroservices starts every connected transport that is not running.
// When one fails, the ones it started are closed again and the error is
// returned.
func (a *App) StartAllMicroservices(ctx context.Context) error {
	a.closeMu.Lock()
	defer a.closeMu.Unlock()
	if a.closed {
		return errors.New("dygo: application is shut down")
	}
	var started []*transportState
	for _, ts := range a.transports {
		if ts.started {
			continue
		}
		if err := ts.t.Listen(ctx, a); err != nil {
			for i := len(started) - 1; i >= 0; i-- {
				_ = started[i].t.Close(ctx)
				started[i].started = false
			}
			return fmt.Errorf("starting %T: %w", ts.t, err)
		}
		ts.started = true
		started = append(started, ts)
	}
	return nil
}

type transportState struct {
	t       Transport
	started bool
}

// closeTransports closes the running transports in reverse order.
func (a *App) closeTransports(ctx context.Context) error {
	var errs []error
	for i := len(a.transports) - 1; i >= 0; i-- {
		ts := a.transports[i]
		if !ts.started {
			continue
		}
		ts.started = false
		if err := ts.t.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("closing %T: %w", ts.t, err))
		}
	}
	return errors.Join(errs...)
}
