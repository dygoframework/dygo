// Package wire is the HTTP code the ai providers share: a hardened client,
// JSON requests with size limits, error mapping and a server-sent events
// reader.
package wire

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dygoframework/dygo/ai"
)

// Limits on what a provider reads, so a broken or hostile server cannot use up
// memory.
const (
	MaxErrorBody = 64 << 10
	MaxBody      = ai.MaxAnswerBytes + 1<<20
	MaxEventLine = 1 << 20
)

// Client returns the HTTP client a provider uses: kept-alive connections,
// TLS 1.2 or newer, and no redirects, because a redirect to another host
// would carry the API key header with it.
func Client() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 32
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{
		Transport: t,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Post sends body as JSON and returns the response when its status is 2xx.
// Otherwise it reads the error body (at most MaxErrorBody) and returns the
// error toError builds from it. The caller closes the response body.
func Post(ctx context.Context, c *http.Client, provider, url string, header http.Header, body any, toError func(*http.Response, []byte) error) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ai.ErrInvalidRequest, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ai.ErrInvalidRequest, err)
	}
	req.Header = header
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &ai.Error{Provider: provider, Kind: ai.ErrUnavailable, Retryable: true, Message: err.Error()}
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res, nil
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, MaxErrorBody))
	return nil, toError(res, data)
}

// StatusError maps an HTTP status to an *ai.Error. kind, when not nil,
// replaces the kind the status alone gives (a provider knows that a 400
// meant "prompt too long", for example).
func StatusError(provider string, res *http.Response, msg string, kind error) *ai.Error {
	e := &ai.Error{Provider: provider, Status: res.StatusCode, Message: msg}
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		e.Kind = ai.ErrAuth
	case res.StatusCode == http.StatusTooManyRequests:
		e.Kind, e.Retryable = ai.ErrRateLimited, true
	case res.StatusCode == http.StatusRequestEntityTooLarge:
		e.Kind = ai.ErrTooLong
	case res.StatusCode == http.StatusRequestTimeout || res.StatusCode >= 500:
		e.Kind, e.Retryable = ai.ErrUnavailable, true
	case res.StatusCode >= 400:
		e.Kind = ai.ErrInvalidRequest
	default: // a redirect: not followed, see Client
		e.Kind = ai.ErrUnavailable
	}
	if kind != nil {
		e.Kind = kind
	}
	e.RetryAfter = RetryAfter(res.Header)
	return e
}

// RetryAfter reads the wait a server asks for: retry-after-ms, or
// Retry-After in seconds or as a date.
func RetryAfter(h http.Header) time.Duration {
	if ms, err := strconv.ParseFloat(h.Get("Retry-After-Ms"), 64); err == nil && ms > 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil && s > 0 {
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// DecodeJSON reads one JSON value of at most MaxBody bytes.
func DecodeJSON(provider string, r io.Reader, v any) error {
	lr := &io.LimitedReader{R: r, N: MaxBody + 1}
	if err := json.NewDecoder(lr).Decode(v); err != nil {
		if lr.N <= 0 {
			return fmt.Errorf("%w (%d bytes)", ai.ErrTooLarge, MaxBody)
		}
		return &ai.Error{Provider: provider, Kind: ai.ErrUnavailable, Message: "bad response: " + err.Error()}
	}
	return nil
}

// Event is one server-sent event.
type Event struct {
	Name string
	Data []byte
}

// Events reads server-sent events from r. A line longer than MaxEventLine
// ends the stream with ai.ErrTooLarge. Data is only valid until the next
// event.
func Events(provider string, r io.Reader) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 16<<10), MaxEventLine)
		var ev Event
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				if len(ev.Data) > 0 || ev.Name != "" {
					if !yield(ev, nil) {
						return
					}
				}
				ev = Event{Data: ev.Data[:0]}
				continue
			}
			field, value, _ := bytes.Cut(line, []byte(":"))
			value = bytes.TrimPrefix(value, []byte(" "))
			switch string(field) {
			case "event":
				ev.Name = string(value)
			case "data":
				if len(ev.Data) > 0 {
					ev.Data = append(ev.Data, '\n')
				}
				ev.Data = append(ev.Data, value...)
			}
		}
		if err := sc.Err(); err != nil {
			if errors.Is(err, bufio.ErrTooLong) {
				err = fmt.Errorf("%w: event line over %d bytes", ai.ErrTooLarge, MaxEventLine)
			} else {
				err = &ai.Error{Provider: provider, Kind: ai.ErrUnavailable, Message: "stream cut: " + err.Error()}
			}
			yield(Event{}, err)
			return
		}
		if len(ev.Data) > 0 {
			yield(ev, nil)
		}
	}
}

// Message returns the "message" of a JSON error body shaped
// {"error": {"message": ..., "type": ..., "code": ...}}, with its type and
// code, or the start of the raw body when it has no such shape.
func Message(body []byte) (msg, typ, code string) {
	var e struct {
		Error struct {
			Message string          `json:"message"`
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message, e.Error.Type, strings.Trim(string(e.Error.Code), `"`)
	}
	return strings.TrimSpace(string(body)), "", ""
}
