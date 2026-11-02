package middleware_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/middleware"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, strings.Repeat("hello ", 300)+middleware.RequestIDFrom(r.Context()))
})

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRequestIDAndLogger(t *testing.T) {
	var buf bytes.Buffer
	h := middleware.RequestID()(middleware.Logger(slog.New(slog.NewTextHandler(&buf, nil)))(ok))

	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-Request-ID", "abc-123")
	w := serve(h, r)
	if w.Header().Get("X-Request-ID") != "abc-123" || !strings.HasSuffix(w.Body.String(), "abc-123") {
		t.Fatalf("%v %q", w.Header(), w.Body.String()[1790:])
	}
	if !strings.Contains(buf.String(), "status=200") || !strings.Contains(buf.String(), "request_id=abc-123") {
		t.Fatal(buf.String())
	}
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-Request-ID", "bad\nid")
	if id := serve(h, r).Header().Get("X-Request-ID"); len(id) != 32 {
		t.Fatalf("generated id %q", id)
	}
}

func TestCORS(t *testing.T) {
	h := middleware.CORS(middleware.CORSOptions{AllowOrigins: []string{"https://app.io"}, AllowCredentials: true, ExposeHeaders: []string{"X-Request-ID"}})(ok)

	r := httptest.NewRequest("OPTIONS", "/users", nil)
	r.Header.Set("Origin", "https://app.io")
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "Authorization")
	w := serve(h, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://app.io" ||
		w.Header().Get("Access-Control-Allow-Headers") != "Authorization" || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("preflight %d %v", w.Code, w.Header())
	}
	r = httptest.NewRequest("GET", "/users", nil)
	r.Header.Set("Origin", "https://evil.io")
	if w := serve(h, r); w.Header().Get("Access-Control-Allow-Origin") != "" || w.Code != 200 {
		t.Fatalf("evil %v", w.Header())
	}
	r.Header.Set("Origin", "https://app.io")
	if w := serve(h, r); w.Header().Get("Access-Control-Expose-Headers") != "X-Request-ID" {
		t.Fatalf("simple %v", w.Header())
	}
}

func TestSecureHeaders(t *testing.T) {
	h := middleware.SecureHeaders(middleware.SecureHeadersOptions{})(ok)
	w := serve(h, httptest.NewRequest("GET", "/", nil))
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Strict-Transport-Security") != "" ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal(w.Header())
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if serve(h, r).Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("no HSTS over https")
	}
}

func TestCSRF(t *testing.T) {
	h := middleware.CSRF(middleware.CSRFOptions{})(ok)
	w := serve(h, httptest.NewRequest("GET", "/", nil))
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "csrf_token" {
		t.Fatal(cookies)
	}
	post := func(header string) int {
		r := httptest.NewRequest("POST", "/", nil)
		r.AddCookie(cookies[0])
		if header != "" {
			r.Header.Set("X-CSRF-Token", header)
		}
		return serve(h, r).Code
	}
	if post("") != 403 || post("wrong") != 403 || post(cookies[0].Value) != 200 {
		t.Fatal("csrf checks")
	}
}

func TestCompress(t *testing.T) {
	h := middleware.Compress()(ok)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Encoding", "br, gzip")
	w := serve(h, r)
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal(w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if !strings.HasPrefix(string(body), "hello hello") {
		t.Fatalf("%q", body)
	}

	small := middleware.Compress()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	if w := serve(small, r); w.Header().Get("Content-Encoding") != "" || w.Code != 202 || w.Body.String() != `{"ok":true}` {
		t.Fatalf("small body: %d %v %q", w.Code, w.Header(), w.Body.String())
	}

	r.Header.Set("Accept-Encoding", "gzip;q=0")
	if serve(h, r).Header().Get("Content-Encoding") != "" {
		t.Fatal("q=0 compressed")
	}
	sse := middleware.Compress()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: x\n\n")
		w.(http.Flusher).Flush()
	}))
	r.Header.Set("Accept-Encoding", "gzip")
	if w := serve(sse, r); w.Header().Get("Content-Encoding") != "" || w.Body.String() != "data: x\n\n" {
		t.Fatal("SSE compressed")
	}
}
