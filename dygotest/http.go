package dygotest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
)

// Server is a test HTTP server running a Dygo application.
type Server struct {
	App    *dygo.HTTPApp
	URL    string
	Client *http.Client
}

// BuildHTTP creates the HTTP application and returns any boot error.
func (b *Builder) BuildHTTP(ctx context.Context) (*dygo.HTTPApp, error) {
	return dygo.Create(ctx, b.root, b.opts...)
}

// CompileHTTP creates the HTTP application, starts it on an httptest server,
// and registers a cleanup that stops the server and shuts the app down. Boot
// errors fail the test.
func (b *Builder) CompileHTTP(t testing.TB) *Server {
	t.Helper()
	app, err := b.BuildHTTP(t.Context())
	if err != nil {
		t.Fatalf("dygotest: %v", err)
	}
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(func() {
		ts.Close()
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("dygotest: shutdown: %v", err)
		}
	})
	return &Server{App: app, URL: ts.URL, Client: ts.Client()}
}

// Req is a request being built.
type Req struct {
	s      *Server
	method string
	path   string
	header http.Header
	body   io.Reader
	edits  []func(*http.Request)
}

// NewRequest starts a request for any method.
func (s *Server) NewRequest(method, path string) *Req {
	return &Req{s: s, method: method, path: path, header: http.Header{}}
}

// GET starts a GET request.
func (s *Server) GET(path string) *Req { return s.NewRequest(http.MethodGet, path) }

// POST starts a POST request.
func (s *Server) POST(path string) *Req { return s.NewRequest(http.MethodPost, path) }

// PUT starts a PUT request.
func (s *Server) PUT(path string) *Req { return s.NewRequest(http.MethodPut, path) }

// PATCH starts a PATCH request.
func (s *Server) PATCH(path string) *Req { return s.NewRequest(http.MethodPatch, path) }

// DELETE starts a DELETE request.
func (s *Server) DELETE(path string) *Req { return s.NewRequest(http.MethodDelete, path) }

// Header sets a request header.
func (r *Req) Header(k, v string) *Req {
	r.header.Set(k, v)
	return r
}

// With changes the request just before it is sent, for helpers that sign
// in or add several headers:
//
//	srv.GET("/orders").With(securitytest.User("7", "user")).Do(t)
func (r *Req) With(edit func(*http.Request)) *Req {
	r.edits = append(r.edits, edit)
	return r
}

// JSON sets a JSON body.
func (r *Req) JSON(v any) *Req {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	r.body = bytes.NewReader(b)
	r.header.Set("Content-Type", "application/json")
	return r
}

// Body sets a raw body and its content type.
func (r *Req) Body(body io.Reader, contentType string) *Req {
	r.body = body
	if contentType != "" {
		r.header.Set("Content-Type", contentType)
	}
	return r
}

// Do sends the request and reads the whole response. Transport errors fail the test.
func (r *Req) Do(t testing.TB) *Res {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), r.method, r.s.URL+r.path, r.body)
	if err != nil {
		t.Fatalf("dygotest: %v", err)
	}
	req.Header = r.header
	for _, edit := range r.edits {
		edit(req)
	}
	resp, err := r.s.Client.Do(req)
	if err != nil {
		t.Fatalf("dygotest: %s %s: %v", r.method, r.path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("dygotest: reading body: %v", err)
	}
	return &Res{Response: resp, Body: body, req: r.method + " " + r.path}
}

// Res is a received response with its body read.
type Res struct {
	*http.Response
	Body []byte
	req  string
}

// Status fails the test unless the status code is want.
func (r *Res) Status(t testing.TB, want int) *Res {
	t.Helper()
	if r.StatusCode != want {
		t.Fatalf("%s: status %d, want %d; body: %s", r.req, r.StatusCode, want, strings.TrimSpace(string(r.Body)))
	}
	return r
}

// JSON decodes the body into v, failing the test on error.
func (r *Res) JSON(t testing.TB, v any) *Res {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("%s: decoding %q: %v", r.req, r.Body, err)
	}
	return r
}

// Text returns the body as a string.
func (r *Res) Text() string { return string(r.Body) }
