// Package micro is the transport-independent part of Dygo microservices:
// message and event handlers declared by providers,
// a client to call other services, and an in-process transport for tests.
// Brokers live in their own modules (github.com/dygoframework/dygo/micro/nats).
//
// A provider with a Patterns method declares handlers, the way a controller
// declares routes:
//
//	func (c *UsersHandlers) Patterns(r micro.Router) {
//		r = r.Use(dygo.Guards(dygo.Ref[*ServiceTokenGuard]()))
//		r.Message("users.get", micro.Handle(c.Get))        // request/response, like @MessagePattern
//		r.Event("orders.created", micro.HandleEvent(c.OnOrder)) // fire and forget, like @EventPattern
//	}
//
//	func (c *UsersHandlers) Get(ctx context.Context, in GetUser) (*User, error)
//
// Payloads are JSON. Guards, interceptors, pipes and filters work as on HTTP
// routes, with ExecutionContext.Kind() == dygo.KindRPC and the message in
// ExecutionContext.RPC(). Errors travel back to the caller in the errs
// error body ({"statusCode": 404, "message": ...}), so an *errs.HTTPError
// returned by a handler comes out of [Send] as the same *errs.HTTPError.
package micro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
)

// Message is one message on the wire.
type Message struct {
	// Pattern is the subject or topic.
	Pattern string
	// Header carries metadata such as a trace or correlation ID.
	Header map[string]string
	// Data is the JSON payload.
	Data []byte
	// Raw is the transport's own message (for example *nats.Msg), or nil.
	Raw any
}

type msgKey struct{}

// MessageFrom returns the message being handled. Handlers get a context that
// carries it.
func MessageFrom(ctx context.Context) (*Message, bool) {
	m, ok := ctx.Value(msgKey{}).(*Message)
	return m, ok
}

// Patterns is a provider that declares message and event handlers.
type Patterns interface {
	Patterns(r Router)
}

// Router registers the handlers of one provider.
type Router interface {
	// Use returns a router whose handlers all get opts (enhancers and
	// metadata), like Router.Use on a controller.
	Use(opts ...dygo.RouteOption) Router
	// Message registers a request/response handler. One handler per pattern.
	Message(pattern string, h Handler, opts ...dygo.RouteOption)
	// Event registers an event handler. A pattern may have several.
	Event(pattern string, h Handler, opts ...dygo.RouteOption)
}

// Handler is a typed handler made by [Handle] or [HandleEvent].
type Handler interface {
	types() (in, out reflect.Type)
	newInput() any
	call(ctx context.Context, in any) (any, error)
	name() string
}

type handler[In, Out any] struct {
	fn    func(ctx context.Context, in In) (Out, error)
	fname string
	noOut bool
}

func (h *handler[In, Out]) types() (reflect.Type, reflect.Type) {
	if h.noOut {
		return reflect.TypeFor[In](), nil
	}
	return reflect.TypeFor[In](), reflect.TypeFor[Out]()
}
func (h *handler[In, Out]) newInput() any { return new(In) }
func (h *handler[In, Out]) name() string  { return h.fname }
func (h *handler[In, Out]) call(ctx context.Context, in any) (any, error) {
	return h.fn(ctx, *in.(*In))
}

// Handle makes a request/response handler. The payload is decoded into In and
// the result encoded as JSON.
func Handle[In, Out any](fn func(ctx context.Context, in In) (Out, error)) Handler {
	return &handler[In, Out]{fn: fn, fname: funcName(fn)}
}

// HandleEvent makes an event handler.
func HandleEvent[In any](fn func(ctx context.Context, in In) error) Handler {
	return &handler[In, struct{}]{
		fn:    func(ctx context.Context, in In) (struct{}, error) { return struct{}{}, fn(ctx, in) },
		fname: funcName(fn), noOut: true,
	}
}

func funcName(fn any) string {
	f := runtime.FuncForPC(reflect.ValueOf(fn).Pointer())
	if f == nil {
		return "?"
	}
	n := f.Name()
	if i := strings.LastIndexByte(n, '/'); i >= 0 {
		n = n[i+1:]
	}
	return strings.TrimSuffix(n, "-fm")
}

// Endpoint is one registered handler with its pipeline.
type Endpoint struct {
	Pattern string
	// Event is true for event handlers.
	Event bool
	h     Handler
	pl    *dygo.Pipeline
}

// Info describes the handler.
func (e *Endpoint) Info() dygo.RouteInfo { return e.pl.Info() }

// Serve handles one message: it decodes the payload, runs the pipeline and
// encodes the reply ([EncodeReply]) for message handlers. A reply is always
// returned for message handlers, holding the error when there is one; err is
// the error itself, for logging, or [dygo.ErrFiltered] when a filter caught
// it.
func (e *Endpoint) Serve(ctx context.Context, m *Message) (reply []byte, err error) {
	ctx = context.WithValue(ctx, msgKey{}, m)
	in := e.h.newInput()
	if len(m.Data) > 0 {
		if derr := json.Unmarshal(m.Data, in); derr != nil {
			err = e.pl.Fail(ctx, m, errs.BadRequest("invalid payload", errs.Cause(derr)))
			return e.reply(nil, err), err
		}
	}
	out, err := e.pl.Run(ctx, m, in, func(ctx context.Context) (any, error) { return e.h.call(ctx, in) })
	return e.reply(out, err), err
}

func (e *Endpoint) reply(out any, err error) []byte {
	if e.Event {
		return nil
	}
	return EncodeReply(out, err)
}

// envelope is the reply on the wire. EncodeReply writes the data case by
// hand, so the payload is not checked by a second encoding pass.
type envelope struct {
	Data  json.RawMessage `json:"data,omitempty"`
	Error *errs.Body      `json:"error,omitempty"`
}

// EncodeReply encodes a handler result or error. Errors are mapped with
// errs.ToBody: an *errs.HTTPError keeps its status and message, anything
// else becomes a 500 without details. [dygo.ErrFiltered] encodes as an empty
// reply.
func EncodeReply(out any, err error) []byte {
	var env envelope
	switch {
	case errors.Is(err, dygo.ErrFiltered):
		return []byte("{}")
	case err != nil:
		b := errs.ToBody(err)
		env.Error = &b
	default:
		data, merr := json.Marshal(out)
		if merr == nil {
			buf := make([]byte, 0, len(data)+len(`{"data":}`))
			buf = append(buf, `{"data":`...)
			buf = append(buf, data...)
			return append(buf, '}')
		}
		b := errs.ToBody(merr)
		env.Error = &b
	}
	b, _ := json.Marshal(env)
	return b
}

// replyIn decodes a reply in one pass: Data holds the caller's pointer.
type replyIn struct {
	Data  any        `json:"data"`
	Error *errs.Body `json:"error"`
}

// DecodeReply is the client side of [EncodeReply]: it decodes the data into
// out (a pointer, or nil to ignore it) or returns the remote error as an
// *errs.HTTPError.
func DecodeReply(reply []byte, out any) error {
	var skip json.RawMessage
	if out == nil {
		out = &skip
	}
	env := replyIn{Data: out}
	err := json.Unmarshal(reply, &env)
	if env.Error != nil {
		return errs.New(env.Error.StatusCode, env.Error.Message, errs.Code(env.Error.Code), errs.Details(env.Error.Details))
	}
	if err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return fmt.Errorf("micro: decoding reply data: %w", err)
		}
		return fmt.Errorf("micro: decoding reply: %w", err)
	}
	return nil
}

// ServerOptions configures [Collect].
type ServerOptions struct {
	// Global enhancers for every handler of the transport, such as
	// dygo.Guards(...).
	Global []dygo.RouteOption
	// InheritAppConfig also runs the app's global enhancers (WithGlobal*
	// options and App* groups).
	InheritAppConfig bool
	// Filter keeps only the patterns it returns true for, when a process
	// runs several transports. Nil keeps all.
	Filter func(pattern string) bool
}

// Handlers is the set of handlers a transport serves.
type Handlers struct {
	messages map[string]*Endpoint
	events   map[string][]*Endpoint
}

// Collect finds every [Patterns] provider of a and builds the pipelines of
// their handlers. Transports call it from Listen. Duplicate message patterns
// and enhancer problems are returned together.
func Collect(a *dygo.App, o ServerOptions) (*Handlers, error) {
	enh, err := dygo.NewEnhancers(a, o.Global, o.InheritAppConfig)
	if err != nil {
		return nil, err
	}
	hs := &Handlers{messages: map[string]*Endpoint{}, events: map[string][]*Endpoint{}}
	var problems []error
	for _, p := range dygo.ProvidersImplementing[Patterns](a) {
		r := &router{owner: p, enh: enh, hs: hs, filter: o.Filter, errs: &problems}
		p.Patterns(r)
	}
	return hs, errors.Join(problems...)
}

// Message returns the handler of a message pattern.
func (hs *Handlers) Message(pattern string) (*Endpoint, bool) {
	e, ok := hs.messages[pattern]
	return e, ok
}

// Events returns the handlers of an event pattern.
func (hs *Handlers) Events(pattern string) []*Endpoint { return hs.events[pattern] }

// MessagePatterns lists the message patterns, sorted.
func (hs *Handlers) MessagePatterns() []string { return slices.Sorted(maps.Keys(hs.messages)) }

// EventPatterns lists the event patterns, sorted.
func (hs *Handlers) EventPatterns() []string { return slices.Sorted(maps.Keys(hs.events)) }

// All lists every endpoint, messages first, each sorted by pattern.
func (hs *Handlers) All() []*Endpoint {
	var out []*Endpoint
	for _, p := range hs.MessagePatterns() {
		out = append(out, hs.messages[p])
	}
	for _, p := range hs.EventPatterns() {
		out = append(out, hs.events[p]...)
	}
	return out
}

type router struct {
	owner  any
	ctrl   []dygo.RouteOption
	enh    *dygo.Enhancers
	hs     *Handlers
	filter func(string) bool
	errs   *[]error
}

func (r *router) Use(opts ...dygo.RouteOption) Router {
	cp := *r
	cp.ctrl = append(slices.Clone(r.ctrl), opts...)
	return &cp
}

func (r *router) Message(pattern string, h Handler, opts ...dygo.RouteOption) {
	r.add(pattern, h, false, opts)
}

func (r *router) Event(pattern string, h Handler, opts ...dygo.RouteOption) {
	r.add(pattern, h, true, opts)
}

func (r *router) add(pattern string, h Handler, event bool, opts []dygo.RouteOption) {
	if r.filter != nil && !r.filter(pattern) {
		return
	}
	fail := func(err error) { *r.errs = append(*r.errs, err) }
	if pattern == "" {
		fail(fmt.Errorf("%T: empty pattern", r.owner))
		return
	}
	if h == nil {
		fail(fmt.Errorf("%T: pattern %q: handler is nil", r.owner, pattern))
		return
	}
	in, out := h.types()
	pl, err := r.enh.Pipeline(dygo.Endpoint{
		Kind: dygo.KindRPC, Pattern: pattern, Owner: r.owner, Handler: h.name(),
		Input: in, Output: out, ControllerOptions: r.ctrl, Options: opts,
	})
	if err != nil {
		fail(err)
		return
	}
	e := &Endpoint{Pattern: pattern, Event: event, h: h, pl: pl}
	if event {
		r.hs.events[pattern] = append(r.hs.events[pattern], e)
		return
	}
	if prev, dup := r.hs.messages[pattern]; dup {
		fail(fmt.Errorf("message pattern %q is handled by both %s and %s", pattern, prev.Info().Handler, h.name()))
		return
	}
	r.hs.messages[pattern] = e
}
