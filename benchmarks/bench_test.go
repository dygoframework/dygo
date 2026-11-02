package benchmarks

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type scenario struct {
	name, method, path, body string
	code                     int
}

var scenarios = []scenario{
	{"static", "GET", "/ping", "", 200},
	{"param", "GET", "/users/42", "", 200},
	{"json", "POST", "/users/42?limit=5", `{"name":"bench"}`, 201},
	{"routing", "GET", LastRoute + "7", "", 200},
}

func newReq(s scenario) *http.Request {
	var r *http.Request
	if s.body != "" {
		r = httptest.NewRequest(s.method, s.path, strings.NewReader(s.body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(s.method, s.path, nil)
	}
	return r
}

// TestSameResponses checks that every server gives the same answer, so the
// benchmarks compare equal work.
func TestSameResponses(t *testing.T) {
	for _, s := range scenarios {
		for _, srv := range Servers() {
			w := httptest.NewRecorder()
			srv.Handler.ServeHTTP(w, newReq(s))
			if w.Code != s.code {
				t.Fatalf("%s %s: status %d: %s", srv.Name, s.name, w.Code, w.Body.String())
			}
			body := strings.TrimSpace(w.Body.String())
			switch s.name {
			case "static":
				if body != "pong" {
					t.Errorf("%s: %q", srv.Name, body)
				}
			case "routing":
				if body != "7" {
					t.Errorf("%s: %q", srv.Name, body)
				}
			default:
				var u User
				if err := json.Unmarshal([]byte(body), &u); err != nil || u.ID != 42 {
					t.Errorf("%s %s: %q %v", srv.Name, s.name, body, err)
				}
				if s.name == "json" && (u.Name != "bench" || u.Limit != 5) {
					t.Errorf("%s json: %+v", srv.Name, u)
				}
			}
		}
	}
}

// nopWriter is a ResponseWriter that discards the body, so the benchmark
// measures the framework and not httptest.ResponseRecorder's buffer.
type nopWriter struct {
	h    http.Header
	code int
}

func (w *nopWriter) Header() http.Header         { return w.h }
func (w *nopWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *nopWriter) WriteHeader(code int)        { w.code = code }

// BenchmarkFrameworks builds each request once and only resets its body per
// iteration, so the numbers are the framework's own cost (routing, binding,
// encoding) rather than httptest.NewRequest's.
func BenchmarkFrameworks(b *testing.B) {
	for _, s := range scenarios {
		for _, srv := range Servers() {
			b.Run(s.name+"/"+srv.Name, func(b *testing.B) {
				b.ReportAllocs()
				w := &nopWriter{h: http.Header{}}
				r := newReq(s)
				body := []byte(s.body)
				br := bytes.NewReader(body)
				rc := io.NopCloser(br)
				for b.Loop() {
					br.Reset(body)
					r.Body = rc
					clear(w.h)
					w.code = 200
					srv.Handler.ServeHTTP(w, r)
					if w.code != s.code {
						b.Fatal(w.code)
					}
				}
			})
		}
	}
}
