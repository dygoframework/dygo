// Package nats is the NATS transport for package micro. [Server] is a
// dygo.Transport that subscribes every message and event pattern of the app;
// [ClientModule] provides a *micro.Client that sends over NATS.
//
//	app, err := dygo.Create(ctx, AppModule())
//	app.ConnectMicroservice(nats.NewServer(nats.Options{URL: nats.DefaultURL, Queue: "users"}))
//	err = app.Listen(ctx, ":9900") // HTTP and NATS in one process
//
// Patterns are NATS subjects, so wildcards ("orders.*", "audit.>") work.
// Set Queue so that each message reaches one instance of the service.
package nats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/micro"
	"github.com/nats-io/nats.go"
)

// DefaultURL is nats://127.0.0.1:4222.
const DefaultURL = nats.DefaultURL

// Options configures [NewServer].
type Options struct {
	// URL of the NATS server, used when Conn is nil.
	URL string
	// Conn is an existing connection; the server does not close it.
	Conn *nats.Conn
	// NATS are extra connection options, used when Conn is nil.
	NATS []nats.Option
	// Queue is the queue group. Instances in the same group share the
	// messages; empty means every instance gets every message.
	Queue string
	// Concurrency bounds how many messages are handled at once (100 by
	// default).
	Concurrency int
	// Server holds the global enhancers and the pattern filter.
	Server micro.ServerOptions
	// OnError gets handler errors (not the ones a filter caught). The
	// default logs them with slog.Default().
	OnError func(ctx context.Context, m *micro.Message, err error)
}

// Server serves the app's micro handlers over NATS.
type Server struct {
	o    Options
	nc   *nats.Conn
	own  bool
	subs []*nats.Subscription
	sem  chan struct{}
	wg   sync.WaitGroup
}

// NewServer returns a NATS transport for app.ConnectMicroservice.
func NewServer(o Options) *Server {
	if o.Concurrency <= 0 {
		o.Concurrency = 100
	}
	if o.OnError == nil {
		o.OnError = func(ctx context.Context, m *micro.Message, err error) {
			slog.Default().ErrorContext(ctx, "nats: handler failed", "pattern", m.Pattern, "err", err)
		}
	}
	return &Server{o: o, sem: make(chan struct{}, o.Concurrency)}
}

// Listen implements dygo.Transport.
func (s *Server) Listen(ctx context.Context, a *dygo.App) error {
	hs, err := micro.Collect(a, s.o.Server)
	if err != nil {
		return err
	}
	s.nc, s.own = s.o.Conn, false
	if s.nc == nil {
		if s.nc, err = nats.Connect(s.o.URL, s.o.NATS...); err != nil {
			return fmt.Errorf("nats: connecting to %s: %w", s.o.URL, err)
		}
		s.own = true
	}
	for _, e := range hs.All() {
		sub, err := s.nc.QueueSubscribe(e.Pattern, s.o.Queue, s.serve(e))
		if err != nil {
			_ = s.Close(ctx)
			return fmt.Errorf("nats: subscribing to %q: %w", e.Pattern, err)
		}
		s.subs = append(s.subs, sub)
	}
	if _, ok := ctx.Deadline(); ok {
		return s.nc.FlushWithContext(ctx)
	}
	return s.nc.FlushTimeout(5 * time.Second)
}

func (s *Server) serve(e *micro.Endpoint) nats.MsgHandler {
	return func(nm *nats.Msg) {
		s.sem <- struct{}{}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.sem }()
			m := &micro.Message{Pattern: nm.Subject, Data: nm.Data, Header: header(nm.Header), Raw: nm}
			ctx := context.Background()
			reply, err := e.Serve(ctx, m)
			if err != nil && !errors.Is(err, dygo.ErrFiltered) {
				s.o.OnError(ctx, m, err)
			}
			if !e.Event && nm.Reply != "" {
				if err := nm.Respond(reply); err != nil {
					s.o.OnError(ctx, m, fmt.Errorf("nats: replying: %w", err))
				}
			}
		}()
	}
}

// Close implements dygo.Transport: it unsubscribes, waits for running
// handlers and closes the connection it opened.
func (s *Server) Close(ctx context.Context) error {
	var errs []error
	for _, sub := range s.subs {
		if err := sub.Unsubscribe(); err != nil && !errors.Is(err, nats.ErrConnectionClosed) {
			errs = append(errs, err)
		}
	}
	s.subs = nil
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		errs = append(errs, fmt.Errorf("nats: waiting for handlers: %w", ctx.Err()))
	}
	if s.own && s.nc != nil {
		s.nc.Close()
	}
	return errors.Join(errs...)
}

func header(h nats.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

// Transport is a micro.ClientTransport over a NATS connection.
type Transport struct {
	nc  *nats.Conn
	own bool
}

// Dial connects to url and returns a client transport that closes the
// connection on Close.
func Dial(url string, opts ...nats.Option) (*Transport, error) {
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats: connecting to %s: %w", url, err)
	}
	return &Transport{nc: nc, own: true}, nil
}

// NewTransport uses an existing connection; Close leaves it open.
func NewTransport(nc *nats.Conn) *Transport { return &Transport{nc: nc} }

// Conn returns the connection.
func (t *Transport) Conn() *nats.Conn { return t.nc }

// Close closes the connection if Dial opened it.
func (t *Transport) Close() {
	if t.own {
		t.nc.Close()
	}
}

func natsMsg(m *micro.Message) *nats.Msg {
	nm := nats.NewMsg(m.Pattern)
	nm.Data = m.Data
	for k, v := range m.Header {
		nm.Header.Set(k, v)
	}
	return nm
}

// Request implements micro.ClientTransport.
func (t *Transport) Request(ctx context.Context, m *micro.Message) ([]byte, error) {
	rm, err := t.nc.RequestMsgWithContext(ctx, natsMsg(m))
	if errors.Is(err, nats.ErrNoResponders) {
		return nil, micro.ErrNoHandler
	}
	if err != nil {
		return nil, err
	}
	return rm.Data, nil
}

// Publish implements micro.ClientTransport.
func (t *Transport) Publish(_ context.Context, m *micro.Message) error {
	return t.nc.PublishMsg(natsMsg(m))
}

// ClientOptions configures [ClientModule].
type ClientOptions struct {
	URL    string
	NATS   []nats.Option
	Client []micro.ClientOption
}

// ClientModule provides a *micro.Client connected to NATS, like
// ClientsModule.register. The connection is made when the client is built
// and closed in OnApplicationShutdown. The module is global.
func ClientModule(o ClientOptions) *dygo.Module {
	return &dygo.Module{
		Name:   "nats-client",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Provide(func() (*clientConn, error) {
				t, err := Dial(o.URL, o.NATS...)
				return &clientConn{t: t}, err
			}),
			dygo.Provide(func(c *clientConn) *micro.Client { return micro.NewClient(c.t, o.Client...) }),
		},
		Exports: []dygo.Token{dygo.TypeOf[*micro.Client]()},
	}
}

type clientConn struct{ t *Transport }

func (c *clientConn) OnApplicationShutdown(context.Context, string) error {
	c.t.Close()
	return nil
}
