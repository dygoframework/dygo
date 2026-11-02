package micro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"
)

// ClientTransport sends messages to a broker. Transports such as micro/nats
// implement it.
type ClientTransport interface {
	// Request sends m and waits for the encoded reply.
	Request(ctx context.Context, m *Message) (reply []byte, err error)
	// Publish sends m without waiting.
	Publish(ctx context.Context, m *Message) error
}

// ErrNoHandler is returned when no service handles a message pattern.
var ErrNoHandler = errors.New("micro: no handler for the pattern")

// ClientOption configures [NewClient].
type ClientOption func(*Client)

// Before adds a hook that runs before every message is sent, to set headers
// or refuse the call.
func Before(fn func(ctx context.Context, m *Message) error) ClientOption {
	return func(c *Client) { c.before = append(c.before, fn) }
}

// Timeout bounds requests whose context has no deadline (5 seconds by
// default).
func Timeout(d time.Duration) ClientOption { return func(c *Client) { c.timeout = d } }

// Client calls other services. Provide one per
// broker connection and inject it.
type Client struct {
	t       ClientTransport
	before  []func(context.Context, *Message) error
	timeout time.Duration
}

// NewClient returns a client that sends through t.
func NewClient(t ClientTransport, opts ...ClientOption) *Client {
	c := &Client{t: t, timeout: 5 * time.Second}
	for _, o := range opts {
		o(c)
	}
	return c
}

type headerKey struct{}

// WithHeader returns ctx with a header that the client sends with every
// message made with it.
func WithHeader(ctx context.Context, key, value string) context.Context {
	h, _ := ctx.Value(headerKey{}).(map[string]string)
	h = maps.Clone(h)
	if h == nil {
		h = map[string]string{}
	}
	h[key] = value
	return context.WithValue(ctx, headerKey{}, h)
}

func (c *Client) message(ctx context.Context, pattern string, in any) (*Message, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("micro: encoding %s payload: %w", pattern, err)
	}
	m := &Message{Pattern: pattern, Data: data}
	if h, ok := ctx.Value(headerKey{}).(map[string]string); ok {
		m.Header = maps.Clone(h)
	}
	for _, fn := range c.before {
		if err := fn(ctx, m); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Send calls the handler of pattern with in and decodes its result into out
// (a pointer, or nil). A remote error is returned as an *errs.HTTPError.
func (c *Client) Send(ctx context.Context, pattern string, in, out any) error {
	m, err := c.message(ctx, pattern, in)
	if err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok && c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	reply, err := c.t.Request(ctx, m)
	if err != nil {
		return fmt.Errorf("micro: %s: %w", pattern, err)
	}
	return DecodeReply(reply, out)
}

// Emit publishes an event; it does not wait for handlers.
func (c *Client) Emit(ctx context.Context, pattern string, in any) error {
	m, err := c.message(ctx, pattern, in)
	if err != nil {
		return err
	}
	return c.t.Publish(ctx, m)
}

// Send is [Client.Send] with a typed result.
func Send[Out any](ctx context.Context, c *Client, pattern string, in any) (Out, error) {
	var out Out
	err := c.Send(ctx, pattern, in, &out)
	return out, err
}
