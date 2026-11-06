package wire

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo/ai"
)

func errorFrom(res *http.Response, body []byte) error {
	msg, _, _ := Message(body)
	return StatusError("p", res, msg, nil)
}

func TestClient(t *testing.T) {
	c := Client()
	if c.Transport.(*http.Transport).TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatal("TLS minimum not set")
	}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "http://example.invalid/steal", http.StatusFound)
	}))
	defer srv.Close()
	_, err := Post(context.Background(), c, "p", srv.URL, http.Header{"X-Api-Key": {"k"}}, map[string]int{}, errorFrom)
	var e *ai.Error
	if !errors.As(err, &e) || e.Status != http.StatusFound || !errors.Is(err, ai.ErrUnavailable) || hits != 1 {
		t.Fatalf("redirect followed or wrong error: %v, hits %d", err, hits)
	}
}

func TestPost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Test") != "1" || string(b) != `{"a":1}` {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()
	res, err := Post(context.Background(), Client(), "p", srv.URL, http.Header{"X-Test": {"1"}}, map[string]int{"a": 1}, errorFrom)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if _, err := Post(context.Background(), Client(), "p", srv.URL, http.Header{}, make(chan int), errorFrom); !errors.Is(err, ai.ErrInvalidRequest) {
		t.Fatalf("got %v", err)
	}
	if _, err := Post(context.Background(), Client(), "p", "http://[::1", http.Header{}, 1, errorFrom); !errors.Is(err, ai.ErrInvalidRequest) {
		t.Fatalf("got %v", err)
	}
}

func TestPostNetworkErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := Post(context.Background(), Client(), "p", url, http.Header{}, 1, errorFrom)
	if !errors.Is(err, ai.ErrUnavailable) || !ai.IsRetryable(err) {
		t.Fatalf("got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Post(ctx, Client(), "p", url, http.Header{}, 1, errorFrom); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestStatusError(t *testing.T) {
	cases := []struct {
		status    int
		kind      error
		retryable bool
	}{
		{401, ai.ErrAuth, false},
		{403, ai.ErrAuth, false},
		{429, ai.ErrRateLimited, true},
		{413, ai.ErrTooLong, false},
		{408, ai.ErrUnavailable, true},
		{500, ai.ErrUnavailable, true},
		{529, ai.ErrUnavailable, true},
		{400, ai.ErrInvalidRequest, false},
	}
	for _, c := range cases {
		e := StatusError("p", &http.Response{StatusCode: c.status, Header: http.Header{}}, "m", nil)
		if !errors.Is(e, c.kind) || e.Retryable != c.retryable {
			t.Errorf("%d: got %v retryable %v", c.status, e, e.Retryable)
		}
	}
	e := StatusError("p", &http.Response{StatusCode: 400, Header: http.Header{"Retry-After": {"2"}}}, "m", ai.ErrTooLong)
	if !errors.Is(e, ai.ErrTooLong) || e.RetryAfter != 2*time.Second {
		t.Fatalf("got %v %v", e, e.RetryAfter)
	}
}

func TestRetryAfter(t *testing.T) {
	cases := map[string]struct {
		h    http.Header
		want time.Duration
	}{
		"ms":      {http.Header{"Retry-After-Ms": {"1500"}}, 1500 * time.Millisecond},
		"seconds": {http.Header{"Retry-After": {"3"}}, 3 * time.Second},
		"none":    {http.Header{}, 0},
		"garbage": {http.Header{"Retry-After": {"soon"}}, 0},
		"past":    {http.Header{"Retry-After": {"Mon, 02 Jan 2006 15:04:05 GMT"}}, 0},
	}
	for name, c := range cases {
		if got := RetryAfter(c.h); got != c.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	if got := RetryAfter(http.Header{"Retry-After": {future}}); got <= 5*time.Second || got > 10*time.Second {
		t.Fatalf("date: got %v", got)
	}
}

func TestDecodeJSON(t *testing.T) {
	var v map[string]int
	if err := DecodeJSON("p", strings.NewReader(`{"a":1}`), &v); err != nil || v["a"] != 1 {
		t.Fatalf("got %v %v", v, err)
	}
	if err := DecodeJSON("p", strings.NewReader(`{`), &v); !errors.Is(err, ai.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	big := `{"a":"` + strings.Repeat("x", MaxBody) + `"}`
	if err := DecodeJSON("p", strings.NewReader(big), &v); !errors.Is(err, ai.ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func collect(t *testing.T, r io.Reader) ([]Event, error) {
	t.Helper()
	var out []Event
	for ev, err := range Events("p", r) {
		if err != nil {
			return out, err
		}
		out = append(out, Event{Name: ev.Name, Data: append([]byte(nil), ev.Data...)})
	}
	return out, nil
}

func TestEvents(t *testing.T) {
	in := ": comment\nevent: a\ndata: one\ndata: two\n\nretry: 5\n\n\ndata:no space\n\ndata: last"
	got, err := collect(t, strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "a" || string(got[0].Data) != "one\ntwo" || got[1].Name != "" || string(got[1].Data) != "no space" || string(got[2].Data) != "last" {
		t.Fatalf("got %+v", got)
	}
}

func TestEventsLimits(t *testing.T) {
	_, err := collect(t, strings.NewReader("data: "+strings.Repeat("x", MaxEventLine+1)+"\n\n"))
	if !errors.Is(err, ai.ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	_, err = collect(t, io.MultiReader(strings.NewReader("data: a\n"), errReader{}))
	if !errors.Is(err, ai.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	for _, err := range Events("p", strings.NewReader("data: a\n\ndata: b\n\n")) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("reset") }

func TestMessage(t *testing.T) {
	msg, typ, code := Message([]byte(`{"error":{"message":"m","type":"t","code":"c"}}`))
	if msg != "m" || typ != "t" || code != "c" {
		t.Fatal(msg, typ, code)
	}
	if _, _, code := Message([]byte(`{"error":{"message":"m","code":42}}`)); code != "42" {
		t.Fatal(code)
	}
	if msg, _, _ := Message([]byte(" plain text \n")); msg != "plain text" {
		t.Fatal(msg)
	}
}
