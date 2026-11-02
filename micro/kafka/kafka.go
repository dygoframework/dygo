// Package kafka is the Kafka transport for package micro, on
// github.com/twmb/franz-go. [Server] is a dygo.Transport
// that consumes the app's patterns as topics in a consumer group; [Dial] and
// [ClientModule] produce to them.
//
//	app.ConnectMicroservice(kafka.NewServer(kafka.Options{Brokers: []string{"localhost:9092"}, GroupID: "users"}))
//
// A pattern that starts with ^ is a regular expression matched against topic
// names, so r.Event(`^audit\..*`, h) handles
// every audit topic, including ones created later. Request/reply works by
// headers: the client sends its reply topic and a correlation ID, and the
// server produces the reply there. Kafka cannot tell that nobody consumes a
// topic, so a request to a pattern no service handles times out instead of
// failing with micro.ErrNoHandler.
package kafka

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/micro"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Header names used on the wire.
const (
	HeaderReplyTopic    = "dygo-reply-topic"
	HeaderCorrelationID = "dygo-correlation-id"
)

// Options configures [NewServer].
type Options struct {
	// Brokers are the seed brokers.
	Brokers []string
	// GroupID is the consumer group; instances with the same group share
	// the partitions. Required.
	GroupID string
	// FromBeginning makes a new group start at the oldest records instead
	// of the newest.
	FromBeginning bool
	// Kafka are extra franz-go options (TLS, SASL, client ID).
	Kafka []kgo.Opt
	// Concurrency bounds how many records are handled at once (100 by
	// default). A batch is finished before the next one is fetched, so
	// offsets are committed only for handled records.
	Concurrency int
	// Server holds the global enhancers and the pattern filter.
	Server micro.ServerOptions
	// OnError gets handler errors; the default logs them with slog.Default().
	OnError func(ctx context.Context, m *micro.Message, err error)
}

// Server serves the app's micro handlers from Kafka topics.
type Server struct {
	o       Options
	cl      *kgo.Client
	exact   map[string][]*micro.Endpoint
	regexps []compiled
	cancel  context.CancelFunc
	done    chan struct{}
}

type compiled struct {
	re *regexp.Regexp
	es []*micro.Endpoint
}

// NewServer returns a Kafka transport for app.ConnectMicroservice.
func NewServer(o Options) *Server {
	if o.Concurrency <= 0 {
		o.Concurrency = 100
	}
	if o.OnError == nil {
		o.OnError = func(ctx context.Context, m *micro.Message, err error) {
			slog.Default().ErrorContext(ctx, "kafka: handler failed", "topic", m.Pattern, "err", err)
		}
	}
	return &Server{o: o}
}

// IsRegexp reports whether a pattern is a regular expression.
func IsRegexp(pattern string) bool { return strings.HasPrefix(pattern, "^") }

// Listen implements dygo.Transport. It returns once the group has its
// partitions, or after 10 seconds.
func (s *Server) Listen(ctx context.Context, a *dygo.App) error {
	if s.o.GroupID == "" {
		return errors.New("kafka: Options.GroupID is required")
	}
	hs, err := micro.Collect(a, s.o.Server)
	if err != nil {
		return err
	}
	s.exact = map[string][]*micro.Endpoint{}
	byRegexp := map[string][]*micro.Endpoint{}
	var problems []error
	for _, e := range hs.All() {
		if IsRegexp(e.Pattern) {
			byRegexp[e.Pattern] = append(byRegexp[e.Pattern], e)
		} else {
			s.exact[e.Pattern] = append(s.exact[e.Pattern], e)
		}
	}
	var topics []string
	for p, es := range byRegexp {
		re, err := regexp.Compile(p)
		if err != nil {
			problems = append(problems, fmt.Errorf("kafka: pattern %q: %w", p, err))
			continue
		}
		s.regexps = append(s.regexps, compiled{re: re, es: es})
		topics = append(topics, p)
	}
	if err := errors.Join(problems...); err != nil {
		return err
	}
	for t := range s.exact {
		if len(s.regexps) > 0 {
			t = "^" + regexp.QuoteMeta(t) + "$" // ConsumeRegex applies to every topic
		}
		topics = append(topics, t)
	}
	if len(topics) == 0 {
		return nil
	}
	assigned := make(chan struct{})
	var once sync.Once
	opts := []kgo.Opt{
		kgo.SeedBrokers(s.o.Brokers...),
		kgo.ConsumerGroup(s.o.GroupID),
		kgo.ConsumeTopics(topics...),
		kgo.OnPartitionsAssigned(func(context.Context, *kgo.Client, map[string][]int32) { once.Do(func() { close(assigned) }) }),
	}
	if len(s.regexps) > 0 {
		opts = append(opts, kgo.ConsumeRegex())
	}
	if s.o.FromBeginning {
		opts = append(opts, kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	}
	s.cl, err = kgo.NewClient(append(opts, s.o.Kafka...)...)
	if err != nil {
		return fmt.Errorf("kafka: %w", err)
	}
	if err := s.cl.Ping(ctx); err != nil {
		s.cl.Close()
		return fmt.Errorf("kafka: connecting: %w", err)
	}
	pctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go s.poll(pctx)
	select {
	case <-assigned:
	case <-time.After(10 * time.Second):
	case <-ctx.Done():
	}
	return nil
}

func (s *Server) endpoints(topic string) []*micro.Endpoint {
	if es, ok := s.exact[topic]; ok {
		return es
	}
	for _, c := range s.regexps {
		if c.re.MatchString(topic) {
			return c.es
		}
	}
	return nil
}

func (s *Server) poll(ctx context.Context) {
	defer close(s.done)
	sem := make(chan struct{}, s.o.Concurrency)
	for {
		fs := s.cl.PollFetches(ctx)
		if ctx.Err() != nil || fs.IsClientClosed() {
			return
		}
		fs.EachError(func(t string, p int32, err error) {
			s.o.OnError(ctx, &micro.Message{Pattern: t}, fmt.Errorf("kafka: fetching partition %d: %w", p, err))
		})
		var wg sync.WaitGroup
		fs.EachRecord(func(r *kgo.Record) {
			for _, e := range s.endpoints(r.Topic) {
				sem <- struct{}{}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					s.handle(e, r)
				}()
			}
		})
		wg.Wait()
	}
}

func (s *Server) handle(e *micro.Endpoint, r *kgo.Record) {
	ctx := context.Background()
	m := &micro.Message{Pattern: r.Topic, Data: r.Value, Header: map[string]string{}, Raw: r}
	for _, h := range r.Headers {
		m.Header[h.Key] = string(h.Value)
	}
	out, err := e.Serve(ctx, m)
	if err != nil && !errors.Is(err, dygo.ErrFiltered) {
		s.o.OnError(ctx, m, err)
	}
	replyTo := m.Header[HeaderReplyTopic]
	if e.Event || replyTo == "" {
		return
	}
	rec := &kgo.Record{Topic: replyTo, Value: out, Headers: []kgo.RecordHeader{
		{Key: HeaderCorrelationID, Value: []byte(m.Header[HeaderCorrelationID])},
	}}
	if err := s.cl.ProduceSync(ctx, rec).FirstErr(); err != nil {
		s.o.OnError(ctx, m, fmt.Errorf("kafka: replying: %w", err))
	}
}

// Close implements dygo.Transport: it stops fetching, waits for the running
// batch, commits and leaves the group.
func (s *Server) Close(ctx context.Context) error {
	if s.cl == nil {
		return nil
	}
	s.cancel()
	select {
	case <-s.done:
	case <-ctx.Done():
	}
	err := s.cl.CommitUncommittedOffsets(ctx)
	s.cl.Close()
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	return err
}

// Transport is a micro.ClientTransport over Kafka. Replies come back on a
// topic of its own, created by Dial and deleted by Close.
type Transport struct {
	cl         *kgo.Client
	replyTopic string

	mu      sync.Mutex
	waiting map[string]chan []byte
	cancel  context.CancelFunc
	done    chan struct{}
}

// Dial connects to the brokers and creates the reply topic.
func Dial(ctx context.Context, brokers []string, opts ...kgo.Opt) (*Transport, error) {
	t := &Transport{replyTopic: "dygo.reply." + randomID(), waiting: map[string]chan []byte{}}
	adm, err := kgo.NewClient(append([]kgo.Opt{kgo.SeedBrokers(brokers...)}, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("kafka: %w", err)
	}
	_, err = kadm.NewClient(adm).CreateTopic(ctx, 1, -1, nil, t.replyTopic)
	adm.Close()
	if err != nil {
		return nil, fmt.Errorf("kafka: creating reply topic: %w", err)
	}
	t.cl, err = kgo.NewClient(append([]kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ConsumeTopics(t.replyTopic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	}, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("kafka: %w", err)
	}
	pctx, cancel := context.WithCancel(context.Background())
	t.cancel, t.done = cancel, make(chan struct{})
	go t.readReplies(pctx)
	return t, nil
}

func (t *Transport) readReplies(ctx context.Context) {
	defer close(t.done)
	for {
		fs := t.cl.PollFetches(ctx)
		if ctx.Err() != nil || fs.IsClientClosed() {
			return
		}
		fs.EachRecord(func(r *kgo.Record) {
			var id string
			for _, h := range r.Headers {
				if h.Key == HeaderCorrelationID {
					id = string(h.Value)
				}
			}
			t.mu.Lock()
			ch := t.waiting[id]
			delete(t.waiting, id)
			t.mu.Unlock()
			if ch != nil {
				ch <- r.Value
			}
		})
	}
}

// Close stops reading replies, deletes the reply topic and closes the
// client.
func (t *Transport) Close(ctx context.Context) error {
	t.cancel()
	<-t.done
	_, err := kadm.NewClient(t.cl).DeleteTopics(ctx, t.replyTopic)
	t.cl.Close()
	return err
}

func record(m *micro.Message, extra ...kgo.RecordHeader) *kgo.Record {
	r := &kgo.Record{Topic: m.Pattern, Value: m.Data, Headers: extra}
	for k, v := range m.Header {
		r.Headers = append(r.Headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	return r
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
	r := record(m,
		kgo.RecordHeader{Key: HeaderReplyTopic, Value: []byte(t.replyTopic)},
		kgo.RecordHeader{Key: HeaderCorrelationID, Value: []byte(id)})
	if err := t.cl.ProduceSync(ctx, r).FirstErr(); err != nil {
		return nil, err
	}
	select {
	case b := <-ch:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Publish implements micro.ClientTransport. It waits until the broker has
// the record.
func (t *Transport) Publish(ctx context.Context, m *micro.Message) error {
	return t.cl.ProduceSync(ctx, record(m)).FirstErr()
}

func randomID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ClientOptions configures [ClientModule].
type ClientOptions struct {
	Brokers []string
	Kafka   []kgo.Opt
	Client  []micro.ClientOption
}

// ClientModule provides a *micro.Client over Kafka. It is global; the client
// closes in OnApplicationShutdown.
func ClientModule(o ClientOptions) *dygo.Module {
	return &dygo.Module{
		Name:   "kafka-client",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Provide(func(ctx context.Context) (*clientConn, error) {
				t, err := Dial(ctx, o.Brokers, o.Kafka...)
				return &clientConn{t: t}, err
			}),
			dygo.Provide(func(c *clientConn) *micro.Client { return micro.NewClient(c.t, o.Client...) }),
		},
		Exports: []dygo.Token{dygo.TypeOf[*micro.Client]()},
	}
}

type clientConn struct{ t *Transport }

func (c *clientConn) OnApplicationShutdown(ctx context.Context, _ string) error {
	return c.t.Close(ctx)
}
