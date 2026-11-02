// Package redis is the Redis pub/sub transport for package micro. [Server] is
// a dygo.Transport that subscribes the app's message and event patterns as
// channels; [Transport] and [ClientModule] send to them.
//
//	app.ConnectMicroservice(redis.NewServer(redis.Options{Addr: "localhost:6379"}))
//
// Patterns with *, ? or [ are Redis glob patterns (PSUBSCRIBE). Pub/sub has
// no queue groups and keeps nothing: every running instance gets every
// message (the client takes the first reply), and a message sent while no
// instance listens is lost. Use micro/nats or micro/kafka to share work
// between instances.
package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/micro"
	goredis "github.com/redis/go-redis/v9"
)

// envelope is a message on a channel.
type envelope struct {
	ID     string            `json:"id,omitempty"`
	Reply  string            `json:"reply,omitempty"`
	Header map[string]string `json:"header,omitempty"`
	Data   json.RawMessage   `json:"data,omitempty"`
}

// reply is an answer on the client's inbox channel.
type reply struct {
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

// Options configures [NewServer].
type Options struct {
	// Addr, Password and DB connect when Client is nil.
	Addr     string
	Password string
	DB       int
	// Client is an existing client; the server does not close it.
	Client *goredis.Client
	// Concurrency bounds how many messages are handled at once (100 by
	// default).
	Concurrency int
	// Server holds the global enhancers and the pattern filter.
	Server micro.ServerOptions
	// OnError gets handler errors; the default logs them with slog.Default().
	OnError func(ctx context.Context, m *micro.Message, err error)
}

// Server serves the app's micro handlers over Redis pub/sub.
type Server struct {
	o      Options
	rdb    *goredis.Client
	own    bool
	pubsub *goredis.PubSub
	sem    chan struct{}
	wg     sync.WaitGroup
	done   chan struct{}
}

// NewServer returns a Redis transport for app.ConnectMicroservice.
func NewServer(o Options) *Server {
	if o.Concurrency <= 0 {
		o.Concurrency = 100
	}
	if o.OnError == nil {
		o.OnError = func(ctx context.Context, m *micro.Message, err error) {
			slog.Default().ErrorContext(ctx, "redis: handler failed", "pattern", m.Pattern, "err", err)
		}
	}
	return &Server{o: o, sem: make(chan struct{}, o.Concurrency)}
}

func isGlob(p string) bool { return strings.ContainsAny(p, "*?[") }

// Listen implements dygo.Transport.
func (s *Server) Listen(ctx context.Context, a *dygo.App) error {
	hs, err := micro.Collect(a, s.o.Server)
	if err != nil {
		return err
	}
	s.rdb, s.own = s.o.Client, false
	if s.rdb == nil {
		s.rdb = goredis.NewClient(&goredis.Options{Addr: s.o.Addr, Password: s.o.Password, DB: s.o.DB})
		s.own = true
	}
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		s.closeClient()
		return fmt.Errorf("redis: connecting: %w", err)
	}
	byPattern := map[string][]*micro.Endpoint{}
	var plain, globs []string
	for _, e := range hs.All() {
		if _, seen := byPattern[e.Pattern]; !seen {
			if isGlob(e.Pattern) {
				globs = append(globs, e.Pattern)
			} else {
				plain = append(plain, e.Pattern)
			}
		}
		byPattern[e.Pattern] = append(byPattern[e.Pattern], e)
	}
	s.pubsub = s.rdb.Subscribe(ctx)
	if len(plain) > 0 {
		if err := s.pubsub.Subscribe(ctx, plain...); err != nil {
			s.closeClient()
			return fmt.Errorf("redis: subscribing: %w", err)
		}
	}
	if len(globs) > 0 {
		if err := s.pubsub.PSubscribe(ctx, globs...); err != nil {
			s.closeClient()
			return fmt.Errorf("redis: subscribing: %w", err)
		}
	}
	s.done = make(chan struct{})
	ch := s.pubsub.Channel()
	go func() {
		defer close(s.done)
		for rm := range ch {
			key := rm.Channel
			if rm.Pattern != "" {
				key = rm.Pattern
			}
			for _, e := range byPattern[key] {
				s.dispatch(e, rm)
			}
		}
	}()
	return nil
}

func (s *Server) dispatch(e *micro.Endpoint, rm *goredis.Message) {
	s.sem <- struct{}{}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.sem }()
		ctx := context.Background()
		var env envelope
		m := &micro.Message{Pattern: rm.Channel, Raw: rm}
		if err := json.Unmarshal([]byte(rm.Payload), &env); err != nil {
			s.o.OnError(ctx, m, fmt.Errorf("redis: bad envelope: %w", err))
			return
		}
		m.Header, m.Data = env.Header, env.Data
		out, err := e.Serve(ctx, m)
		if err != nil && !errors.Is(err, dygo.ErrFiltered) {
			s.o.OnError(ctx, m, err)
		}
		if e.Event || env.Reply == "" {
			return
		}
		if err := s.rdb.Publish(ctx, env.Reply, encodeReply(env.ID, out)).Err(); err != nil {
			s.o.OnError(ctx, m, fmt.Errorf("redis: replying: %w", err))
		}
	}()
}

// Close implements dygo.Transport: it unsubscribes, waits for running
// handlers and closes the client it opened.
func (s *Server) Close(ctx context.Context) error {
	var err error
	if s.pubsub != nil {
		err = s.pubsub.Close()
		<-s.done
	}
	finished := make(chan struct{})
	go func() { s.wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-ctx.Done():
		err = errors.Join(err, fmt.Errorf("redis: waiting for handlers: %w", ctx.Err()))
	}
	s.closeClient()
	return err
}

func (s *Server) closeClient() {
	if s.own && s.rdb != nil {
		_ = s.rdb.Close()
	}
}

// Transport is a micro.ClientTransport over Redis pub/sub. Replies come back
// on one inbox channel per transport.
type Transport struct {
	rdb    *goredis.Client
	own    bool
	inbox  string
	pubsub *goredis.PubSub

	mu      sync.Mutex
	waiting map[string]chan []byte
}

// NewTransport returns a client transport on rdb. own closes rdb on Close.
func NewTransport(ctx context.Context, rdb *goredis.Client, own bool) (*Transport, error) {
	t := &Transport{rdb: rdb, own: own, inbox: "_dygo.inbox." + randomID(), waiting: map[string]chan []byte{}}
	t.pubsub = rdb.Subscribe(ctx, t.inbox)
	if _, err := t.pubsub.Receive(ctx); err != nil { // wait for the subscription
		_ = t.pubsub.Close()
		return nil, fmt.Errorf("redis: subscribing to the inbox: %w", err)
	}
	go func() {
		for m := range t.pubsub.Channel() {
			var r reply
			if json.Unmarshal([]byte(m.Payload), &r) != nil {
				continue
			}
			t.mu.Lock()
			ch := t.waiting[r.ID]
			delete(t.waiting, r.ID)
			t.mu.Unlock()
			if ch != nil {
				ch <- r.Data
			}
		}
	}()
	return t, nil
}

// Dial connects to addr and returns a client transport.
func Dial(ctx context.Context, opts *goredis.Options) (*Transport, error) {
	return NewTransport(ctx, goredis.NewClient(opts), true)
}

// Close stops the inbox and closes the client if it is owned.
func (t *Transport) Close() error {
	err := t.pubsub.Close()
	if t.own {
		err = errors.Join(err, t.rdb.Close())
	}
	return err
}

func (t *Transport) publish(ctx context.Context, m *micro.Message, env envelope) (int64, error) {
	env.Header = m.Header
	b, err := encodeEnvelope(env, m.Data)
	if err != nil {
		return 0, err
	}
	return t.rdb.Publish(ctx, m.Pattern, b).Result()
}

// encodeEnvelope encodes env with data, which is already JSON (the client
// encoded it), so it is appended as is rather than checked again by
// json.Marshal.
func encodeEnvelope(env envelope, data []byte) ([]byte, error) {
	env.Data = nil
	b, err := json.Marshal(env)
	if err != nil || len(data) == 0 {
		return b, err
	}
	b = b[:len(b)-1] // drop the closing brace
	if len(b) > 1 {
		b = append(b, ',')
	}
	b = append(b, `"data":`...)
	b = append(b, data...)
	return append(b, '}'), nil
}

// encodeReply encodes a reply; data is the encoded micro reply.
func encodeReply(id string, data []byte) []byte {
	q, _ := json.Marshal(id)
	b := make([]byte, 0, len(q)+len(data)+len(`{"id":,"data":}`))
	b = append(b, `{"id":`...)
	b = append(b, q...)
	b = append(b, `,"data":`...)
	b = append(b, data...)
	return append(b, '}')
}

// Request implements micro.ClientTransport.
func (t *Transport) Request(ctx context.Context, m *micro.Message) ([]byte, error) {
	id := randomID()
	ch := make(chan []byte, 1)
	t.mu.Lock()
	t.waiting[id] = ch
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.waiting, id)
		t.mu.Unlock()
	}()
	n, err := t.publish(ctx, m, envelope{ID: id, Reply: t.inbox})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, micro.ErrNoHandler
	}
	select {
	case b := <-ch:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Publish implements micro.ClientTransport.
func (t *Transport) Publish(ctx context.Context, m *micro.Message) error {
	_, err := t.publish(ctx, m, envelope{})
	return err
}

func randomID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ClientOptions configures [ClientModule].
type ClientOptions struct {
	Redis  goredis.Options
	Client []micro.ClientOption
}

// ClientModule provides a *micro.Client over Redis. It is global; the
// connection closes in OnApplicationShutdown.
func ClientModule(o ClientOptions) *dygo.Module {
	return &dygo.Module{
		Name:   "redis-client",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Provide(func(ctx context.Context) (*clientConn, error) {
				t, err := Dial(ctx, &o.Redis)
				return &clientConn{t: t}, err
			}),
			dygo.Provide(func(c *clientConn) *micro.Client { return micro.NewClient(c.t, o.Client...) }),
		},
		Exports: []dygo.Token{dygo.TypeOf[*micro.Client]()},
	}
}

type clientConn struct{ t *Transport }

func (c *clientConn) OnApplicationShutdown(context.Context, string) error { return c.t.Close() }
