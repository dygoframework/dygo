// Package ws adds WebSocket gateways on
// github.com/coder/websocket. A provider with a Gateway method handles
// events sent by clients:
//
//	func (c *Chat) Gateway(g ws.Router) {
//		g = g.Use(dygo.Guards(dygo.Ref[*WSAuthGuard]()))
//		g.On("chat.send", ws.Handle(c.send)) // like @SubscribeMessage("chat.send")
//	}
//
//	func (c *Chat) send(ctx context.Context, client *ws.Client, in Message) (Ack, error) {
//		client.Join("room:" + in.Room)
//		return Ack{OK: true}, c.server.To("room:"+in.Room).Emit(ctx, "chat.message", in)
//	}
//
// Frames are JSON: {"event": "chat.send", "data": {...}, "id": 1}. A
// handler's result is sent back with the same event and id (an ack); an
// error is sent as {"event": "exception", "data": <error body>, "id": 1}.
// Gateways may implement [ConnectionHandler] and [DisconnectHandler].
//
// Import [Module] in the root module; it serves the WebSocket endpoint on
// the app's HTTP server (Options.Path, "/ws" by default). Guards,
// interceptors, pipes and filters run with ExecutionContext.Kind() ==
// dygo.KindWS and the client in ExecutionContext.WS().Client().
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
)

// Gateway is a provider that handles client events.
type Gateway interface {
	Gateway(g Router)
}

// ConnectionHandler is called when a client connects, like
// OnGatewayConnection. An error refuses the client: the connection is closed
// with status 1008 (policy violation) and the error's message.
type ConnectionHandler interface {
	OnConnection(ctx context.Context, c *Client) error
}

// DisconnectHandler is called when a client is gone, with the reason.
type DisconnectHandler interface {
	OnDisconnect(c *Client, reason DisconnectReason)
}

// DisconnectReason tells why a client went away.
type DisconnectReason struct {
	// Code is the WebSocket close status, or -1 when the connection broke
	// without a close frame.
	Code int
	// Text is the close reason or the error.
	Text string
}

// Router registers the event handlers of one gateway.
type Router interface {
	Use(opts ...dygo.RouteOption) Router
	On(event string, h Handler, opts ...dygo.RouteOption)
}

// Handler is made by [Handle] or [HandleNoReply].
type Handler interface {
	types() (in, out reflect.Type)
	newInput() any
	call(ctx context.Context, c *Client, in any) (any, error)
	name() string
}

type handler[In, Out any] struct {
	fn    func(ctx context.Context, c *Client, in In) (Out, error)
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
func (h *handler[In, Out]) call(ctx context.Context, c *Client, in any) (any, error) {
	out, err := h.fn(ctx, c, *in.(*In))
	if err != nil || h.noOut {
		return nil, err
	}
	return out, nil
}

// Handle makes a handler whose result is sent back as an ack.
func Handle[In, Out any](fn func(ctx context.Context, c *Client, in In) (Out, error)) Handler {
	return &handler[In, Out]{fn: fn, fname: funcName(fn)}
}

// HandleNoReply makes a handler that sends nothing back unless it fails.
func HandleNoReply[In any](fn func(ctx context.Context, c *Client, in In) error) Handler {
	return &handler[In, struct{}]{
		fn:    func(ctx context.Context, c *Client, in In) (struct{}, error) { return struct{}{}, fn(ctx, c, in) },
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

type perMessageKey struct{}

// PerMessageScope gives every message its own request scope:
// dygo.Scoped[T].Get(ctx) in a handler builds request scoped providers for
// that message only, and their OnRequestEnd hooks run once the handler
// returned and its ack was sent. The gateway itself stays a singleton. Use it
// on a gateway (g.Use(ws.PerMessageScope())) or one event. *http.Request and
// the context ID strategy see the handshake request, so durable providers
// resolve for the client's tenant.
//
// Without it, request scoped providers resolve in the handshake request's
// scope and live as long as the connection.
func PerMessageScope() dygo.RouteOption { return dygo.SetMetadata(perMessageKey{}, true) }

// Options configures [Module].
type Options struct {
	// Path of the endpoint on the HTTP server, "/ws" by default.
	Path string
	// OriginPatterns are the host patterns allowed besides the request's own
	// host, as in websocket.AcceptOptions.
	OriginPatterns []string
	// Global enhancers for every gateway handler.
	Global []dygo.RouteOption
	// InheritAppConfig also runs the app's global enhancers.
	InheritAppConfig bool
	// ReadLimit is the largest frame accepted, 64 KiB by default.
	ReadLimit int64
	// WriteTimeout bounds each frame sent, 10 seconds by default.
	WriteTimeout time.Duration
	// PingInterval keeps connections alive and detects dead ones (30
	// seconds by default, negative to turn off).
	PingInterval time.Duration
	// OnError gets handler errors (they are also sent to the client). The
	// default logs 5xx errors with slog.Default().
	OnError func(ctx context.Context, c *Client, event string, err error)
}

// Module provides the *Server and serves it at Options.Path. It is global.
func Module(o Options) *dygo.Module {
	return &dygo.Module{
		Name:   "ws",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Provide(func(ref *dygo.ModuleRef) *Server {
				s := NewServer(o)
				s.ref = ref
				return s
			}),
		},
		Controllers: []dygo.Provider{dygo.Provide(func(s *Server) *endpoint { return &endpoint{s: s} })},
		Exports:     []dygo.Token{dygo.TypeOf[*Server]()},
	}
}

type endpoint struct{ s *Server }

func (e *endpoint) Routes(r dygo.Router) {
	r.Get(e.s.o.Path, dygo.HandleRaw(func(w http.ResponseWriter, r *http.Request) error {
		e.s.ServeHTTP(w, r)
		return nil
	}), dygo.Version(dygo.VersionNeutral))
}

// Server accepts WebSocket clients and dispatches their events to gateways.
type Server struct {
	o   Options
	ref *dygo.ModuleRef
	app *dygo.App

	handlers map[string]*route
	conns    []ConnectionHandler
	discs    []DisconnectHandler

	mu      sync.RWMutex
	clients map[*Client]struct{}
	rooms   map[string]map[*Client]struct{}
	closing bool
	wg      sync.WaitGroup
	nextID  atomic.Uint64
}

type route struct {
	event      string
	h          Handler
	pl         *dygo.Pipeline
	perMessage bool
}

// NewServer returns a server outside of [Module]. Call Register before
// serving it with ServeHTTP.
func NewServer(o Options) *Server {
	if o.Path == "" {
		o.Path = "/ws"
	}
	if o.ReadLimit == 0 {
		o.ReadLimit = 64 << 10
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.PingInterval == 0 {
		o.PingInterval = 30 * time.Second
	}
	if o.OnError == nil {
		o.OnError = func(ctx context.Context, c *Client, event string, err error) {
			if errs.ToBody(err).StatusCode >= 500 {
				slog.Default().ErrorContext(ctx, "ws: handler failed", "event", event, "client", c.ID(), "err", err)
			}
		}
	}
	return &Server{
		o: o, handlers: map[string]*route{},
		clients: map[*Client]struct{}{}, rooms: map[string]map[*Client]struct{}{},
	}
}

// OnModuleInit finds the gateways and builds their pipelines.
func (s *Server) OnModuleInit(context.Context) error {
	return s.Register(s.ref.App())
}

// BeforeApplicationShutdown closes every client with 1001 (going away) and
// waits for running handlers.
func (s *Server) BeforeApplicationShutdown(ctx context.Context, _ string) error {
	return s.Close(ctx)
}

// Register finds the [Gateway] providers of a and builds the pipelines of
// their handlers. Two handlers for one event are an error.
func (s *Server) Register(a *dygo.App) error {
	enh, err := dygo.NewEnhancers(a, s.o.Global, s.o.InheritAppConfig)
	if err != nil {
		return err
	}
	s.app = a
	var problems []error
	for _, g := range dygo.ProvidersImplementing[Gateway](a) {
		g.Gateway(&router{s: s, owner: g, enh: enh, errs: &problems})
		if h, ok := g.(ConnectionHandler); ok {
			s.conns = append(s.conns, h)
		}
		if h, ok := g.(DisconnectHandler); ok {
			s.discs = append(s.discs, h)
		}
	}
	return errors.Join(problems...)
}

// Events lists the handled events, sorted.
func (s *Server) Events() []string {
	var out []string
	for e := range s.handlers {
		out = append(out, e)
	}
	slices.Sort(out)
	return out
}

type router struct {
	s     *Server
	owner any
	ctrl  []dygo.RouteOption
	enh   *dygo.Enhancers
	errs  *[]error
}

func (r *router) Use(opts ...dygo.RouteOption) Router {
	cp := *r
	cp.ctrl = append(slices.Clone(r.ctrl), opts...)
	return &cp
}

func (r *router) On(event string, h Handler, opts ...dygo.RouteOption) {
	fail := func(err error) { *r.errs = append(*r.errs, err) }
	switch {
	case event == "" || event == "exception":
		fail(fmt.Errorf("%T: event name %q is reserved or empty", r.owner, event))
		return
	case h == nil:
		fail(fmt.Errorf("%T: event %q: handler is nil", r.owner, event))
		return
	}
	in, out := h.types()
	pl, err := r.enh.Pipeline(dygo.Endpoint{
		Kind: dygo.KindWS, Pattern: event, Owner: r.owner, Handler: h.name(),
		Input: in, Output: out, ControllerOptions: r.ctrl, Options: opts,
	})
	if err != nil {
		fail(err)
		return
	}
	if prev, dup := r.s.handlers[event]; dup {
		fail(fmt.Errorf("ws event %q is handled by both %s and %s", event, prev.h.name(), h.name()))
		return
	}
	perMessage, _ := pl.Info().Metadata(perMessageKey{})
	r.s.handlers[event] = &route{event: event, h: h, pl: pl, perMessage: perMessage == true}
}

// frame is a message on the wire.
type frame struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
	ID    any             `json:"id,omitempty"`
}

// ServeHTTP upgrades the request and serves the client until it leaves.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.o.OriginPatterns})
	if err != nil {
		return // Accept wrote the response
	}
	conn.SetReadLimit(s.o.ReadLimit)
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	c := &Client{s: s, conn: conn, req: r, id: fmt.Sprintf("c%d", s.nextID.Add(1)), rooms: map[string]struct{}{}}
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	for _, h := range s.conns {
		if err := h.OnConnection(ctx, c); err != nil {
			s.remove(c)
			_ = conn.Close(websocket.StatusPolicyViolation, truncate(errs.ToBody(err).Message))
			return
		}
	}
	if s.o.PingInterval > 0 {
		go c.keepAlive(ctx, s.o.PingInterval)
	}
	reason := c.readLoop(ctx)
	s.remove(c)
	cancel()
	_ = conn.CloseNow()
	for _, h := range s.discs {
		h.OnDisconnect(c, reason)
	}
}

func truncate(s string) string {
	if len(s) > 120 { // a close reason must fit in a control frame
		return s[:120]
	}
	return s
}

func (c *Client) readLoop(ctx context.Context) DisconnectReason {
	for {
		typ, b, err := c.conn.Read(ctx)
		if err != nil {
			if st := websocket.CloseStatus(err); st != -1 {
				var ce websocket.CloseError
				errors.As(err, &ce)
				return DisconnectReason{Code: int(st), Text: ce.Reason}
			}
			if r := c.closeReason.Load(); r != nil {
				return *r
			}
			return DisconnectReason{Code: -1, Text: err.Error()}
		}
		if typ != websocket.MessageText {
			c.sendError(ctx, nil, errs.BadRequest("binary frames are not supported"))
			continue
		}
		var f frame
		if err := json.Unmarshal(b, &f); err != nil || f.Event == "" {
			c.sendError(ctx, nil, errs.BadRequest("invalid frame"))
			continue
		}
		c.s.dispatch(ctx, c, &f)
	}
}

// dispatch runs one event's handler. Events of one client are handled in
// order.
func (s *Server) dispatch(ctx context.Context, c *Client, f *frame) {
	rt, ok := s.handlers[f.Event]
	if !ok {
		c.sendError(ctx, f.ID, errs.NotFound("no handler for event "+f.Event))
		return
	}
	if rt.perMessage {
		var end func()
		ctx, end = s.app.WithRequestScope(ctx, c.req)
		defer end()
	}
	in := rt.h.newInput()
	if len(f.Data) > 0 {
		if err := json.Unmarshal(f.Data, in); err != nil {
			if err := rt.pl.Fail(ctx, c, errs.BadRequest("invalid payload", errs.Cause(err))); !errors.Is(err, dygo.ErrFiltered) {
				c.sendError(ctx, f.ID, err)
			}
			return
		}
	}
	out, err := rt.pl.Run(ctx, c, in, func(ctx context.Context) (any, error) { return rt.h.call(ctx, c, in) })
	switch {
	case errors.Is(err, dygo.ErrFiltered):
	case err != nil:
		s.o.OnError(ctx, c, f.Event, err)
		c.sendError(ctx, f.ID, err)
	case out != nil:
		if err := c.send(ctx, f.Event, out, f.ID); err != nil {
			s.o.OnError(ctx, c, f.Event, err)
		}
	}
}

func (s *Server) remove(c *Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, c)
	c.mu.Lock()
	for room := range c.rooms {
		s.leave(room, c)
	}
	c.mu.Unlock()
}

func (s *Server) leave(room string, c *Client) {
	if m := s.rooms[room]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(s.rooms, room)
		}
	}
}

// Clients returns the connected clients.
func (s *Server) Clients() []*Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Client, 0, len(s.clients))
	for c := range s.clients {
		out = append(out, c)
	}
	return out
}

// Broadcast sends an event to every client.
func (s *Server) Broadcast(ctx context.Context, event string, data any) error {
	return emitAll(ctx, s.Clients(), event, data, nil)
}

// To selects the clients of a room.
func (s *Server) To(room string) Room { return Room{s: s, name: room} }

// Room is a named group of clients.
type Room struct {
	s    *Server
	name string
}

// Clients returns the room's clients.
func (r Room) Clients() []*Client {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := make([]*Client, 0, len(r.s.rooms[r.name]))
	for c := range r.s.rooms[r.name] {
		out = append(out, c)
	}
	return out
}

// Emit sends an event to every client in the room.
func (r Room) Emit(ctx context.Context, event string, data any) error {
	return emitAll(ctx, r.Clients(), event, data, nil)
}

// Except sends an event to every client in the room but one, usually the
// sender.
func (r Room) Except(ctx context.Context, skip *Client, event string, data any) error {
	return emitAll(ctx, r.Clients(), event, data, skip)
}

func emitAll(ctx context.Context, cs []*Client, event string, data any, skip *Client) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("ws: encoding %s: %w", event, err)
	}
	fb, err := json.Marshal(frame{Event: event, Data: b})
	if err != nil {
		return fmt.Errorf("ws: encoding %s: %w", event, err)
	}
	var es []error
	for _, c := range cs {
		if c != skip {
			if err := c.writeFrame(ctx, fb); err != nil {
				es = append(es, err)
			}
		}
	}
	return errors.Join(es...)
}

// Close closes every client with 1001 (going away), refuses new ones and
// waits for the connections to end until ctx is done.
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	for _, c := range s.Clients() {
		go func() { _ = c.Close(websocket.StatusGoingAway, "server shutting down") }()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("ws: waiting for connections: %w", ctx.Err())
	}
}

// Client is one connection.
type Client struct {
	s    *Server
	conn *websocket.Conn
	req  *http.Request
	id   string

	mu     sync.Mutex
	rooms  map[string]struct{}
	values map[any]any

	writeMu     sync.Mutex
	closeReason atomic.Pointer[DisconnectReason]
}

// ID identifies the client within the server.
func (c *Client) ID() string { return c.id }

// Request is the handshake request, for headers, cookies and query
// parameters (a token, for example).
func (c *Client) Request() *http.Request { return c.req }

// Set stores a value on the client, such as the authenticated user.
func (c *Client) Set(key, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = map[any]any{}
	}
	c.values[key] = value
}

// Get returns a value stored with Set.
func (c *Client) Get(key any) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.values[key]
	return v, ok
}

// Join adds the client to a room.
func (c *Client) Join(room string) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if _, ok := c.s.clients[c]; !ok {
		return // gone or not yet accepted
	}
	m := c.s.rooms[room]
	if m == nil {
		m = map[*Client]struct{}{}
		c.s.rooms[room] = m
	}
	m[c] = struct{}{}
	c.mu.Lock()
	c.rooms[room] = struct{}{}
	c.mu.Unlock()
}

// Leave removes the client from a room.
func (c *Client) Leave(room string) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.leave(room, c)
	c.mu.Lock()
	delete(c.rooms, room)
	c.mu.Unlock()
}

// Rooms lists the client's rooms, sorted.
func (c *Client) Rooms() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.rooms))
	for r := range c.rooms {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// Emit sends an event to this client.
func (c *Client) Emit(ctx context.Context, event string, data any) error {
	return c.send(ctx, event, data, nil)
}

// Close closes the connection with a status code and reason; the
// disconnect handlers get them.
func (c *Client) Close(code websocket.StatusCode, reason string) error {
	c.closeReason.CompareAndSwap(nil, &DisconnectReason{Code: int(code), Text: reason})
	return c.conn.Close(code, truncate(reason))
}

func (c *Client) send(ctx context.Context, event string, data any, id any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("ws: encoding %s: %w", event, err)
	}
	return c.write(ctx, frame{Event: event, Data: b, ID: id})
}

func (c *Client) sendError(ctx context.Context, id any, err error) {
	b := errs.ToBody(err)
	if b.StatusCode == errs.StatusClientClosedRequest {
		return
	}
	_ = c.send(ctx, "exception", b, id)
}

func (c *Client) write(ctx context.Context, f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return c.writeFrame(ctx, b)
}

// writeFrame sends an encoded frame. emitAll encodes a frame once for all
// its clients.
func (c *Client) writeFrame(ctx context.Context, b []byte) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.s.o.WriteTimeout)
	defer cancel()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Write(ctx, websocket.MessageText, b)
}

func (c *Client) keepAlive(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, every)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil && ctx.Err() == nil {
				_ = c.Close(websocket.StatusPolicyViolation, "ping timeout")
				return
			}
		}
	}
}
