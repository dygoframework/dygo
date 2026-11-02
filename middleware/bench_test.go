package middleware_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/middleware"
)

// BenchmarkStack is the example app's middleware stack around a small JSON
// response, with the request log level on.
func BenchmarkStack(b *testing.B) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1,"name":"ada"}`)
	})
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	stack := middleware.RequestID()(middleware.Logger(log)(middleware.SecureHeaders(middleware.SecureHeadersOptions{})(
		middleware.CORS(middleware.CORSOptions{AllowOrigins: []string{"https://app.io"}})(middleware.Compress()(h)))))
	b.ReportAllocs()
	for b.Loop() {
		r := httptest.NewRequest("GET", "/users", strings.NewReader(""))
		r.Header.Set("Origin", "https://app.io")
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		stack.ServeHTTP(w, r)
	}
}

// BenchmarkBaseline is the handler alone, to subtract.
func BenchmarkBaseline(b *testing.B) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1,"name":"ada"}`)
	})
	b.ReportAllocs()
	for b.Loop() {
		r := httptest.NewRequest("GET", "/users", strings.NewReader(""))
		r.Header.Set("Origin", "https://app.io")
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
	}
}
