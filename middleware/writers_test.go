package middleware_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/middleware"
)

// These tests cover the response writer wrappers: status capture, ReadFrom,
// Flush, Hijack and Unwrap, which need a real server for some paths.

func TestLoggerStatusAndLevel(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    []string
	}{
		{"empty body is 200", func(http.ResponseWriter, *http.Request) {}, []string{"level=INFO", "status=200"}},
		{"first status wins", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			w.WriteHeader(http.StatusOK) // ignored by the recorder and net/http
		}, []string{"level=ERROR", "status=502"}},
		{"read from", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.(io.ReaderFrom).ReadFrom(strings.NewReader("abc"))
		}, []string{"status=200", "bytes=3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := middleware.Logger(slog.New(slog.NewTextHandler(&buf, nil)))(tt.handler)
			serve(h, httptest.NewRequest("GET", "/", nil))
			for _, w := range tt.want {
				if !strings.Contains(buf.String(), w) {
					t.Errorf("log lacks %q: %s", w, buf.String())
				}
			}
		})
	}
}

func TestLoggerDefaultAndDisabled(t *testing.T) {
	serve(middleware.Logger(nil)(ok), httptest.NewRequest("GET", "/", nil))
	var buf bytes.Buffer
	quiet := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	serve(middleware.Logger(quiet)(ok), httptest.NewRequest("GET", "/", nil))
	if buf.Len() != 0 {
		t.Fatalf("logged below the level: %s", buf.String())
	}
}

// onlyWriter is a ResponseWriter without ReadFrom, Flush or Hijack.
type onlyWriter struct {
	h    http.Header
	body bytes.Buffer
}

func (w *onlyWriter) Header() http.Header         { return w.h }
func (w *onlyWriter) Write(b []byte) (int, error) { return w.body.Write(b) }
func (w *onlyWriter) WriteHeader(int)             {}

func TestRecorderReadFromWithoutReaderFrom(t *testing.T) {
	var buf bytes.Buffer
	h := middleware.Logger(slog.New(slog.NewTextHandler(&buf, nil)))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.(io.ReaderFrom).ReadFrom(strings.NewReader("abcd"))
		w.(http.Flusher).Flush() // a no-op when the writer underneath cannot flush
	}))
	w := &onlyWriter{h: http.Header{}}
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.body.String() != "abcd" || !strings.Contains(buf.String(), "bytes=4") {
		t.Fatalf("body %q, log %s", w.body.String(), buf.String())
	}
}

// hijacker answers with a raw HTTP response after taking over the connection.
var hijacker = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 6\r\nConnection: close\r\n\r\nraw ok")
	_ = rw.Flush()
})

func TestHijackThroughWrappers(t *testing.T) {
	tests := []struct {
		name string
		mw   func(http.Handler) http.Handler
	}{
		{"logger", middleware.Logger(slog.New(slog.NewTextHandler(io.Discard, nil)))},
		{"compress", middleware.Compress()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.mw(hijacker))
			defer srv.Close()
			conn, err := net.Dial("tcp", srv.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\nAccept-Encoding: gzip\r\n\r\n")
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			if string(body) != "raw ok" {
				t.Fatalf("body %q", body)
			}
		})
	}
}

func TestFlushThroughLogger(t *testing.T) {
	h := middleware.Logger(slog.New(slog.NewTextHandler(io.Discard, nil)))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "a")
		w.(http.Flusher).Flush()
		if _, ok := w.(interface{ Unwrap() http.ResponseWriter }); !ok {
			t.Error("no Unwrap")
		}
	}))
	if w := serve(h, httptest.NewRequest("GET", "/", nil)); !w.Flushed || w.Body.String() != "a" {
		t.Fatalf("flushed %v body %q", w.Flushed, w.Body.String())
	}
}

func TestCompressStatuses(t *testing.T) {
	big := strings.Repeat("x", 4096)
	tests := []struct {
		name    string
		handler http.HandlerFunc
		gzip    bool
		code    int
	}{
		{"flush before writing", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, big)
			if _, ok := w.(interface{ Unwrap() http.ResponseWriter }); !ok {
				t.Error("no Unwrap")
			}
		}, true, http.StatusOK},
		{"status only", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			w.WriteHeader(http.StatusOK)
		}, false, http.StatusTeapot},
		{"already encoded", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, big)
		}, false, http.StatusOK},
		{"image", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, big)
		}, false, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Accept-Encoding", "gzip")
			w := serve(middleware.Compress()(tt.handler), r)
			if w.Code != tt.code {
				t.Fatalf("code %d, want %d", w.Code, tt.code)
			}
			if got := w.Header().Get("Content-Encoding") == "gzip"; got != tt.gzip {
				t.Fatalf("gzip %v, want %v (%v)", got, tt.gzip, w.Header())
			}
			if tt.gzip {
				zr, err := gzip.NewReader(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				if b, _ := io.ReadAll(zr); string(b) != big {
					t.Fatalf("body %d bytes", len(b))
				}
			}
		})
	}
}

func TestAcceptsGzip(t *testing.T) {
	tests := []struct {
		header string
		gzip   bool
	}{
		{"*", true},
		{"gzip;q=0.5", true},
		{"gzip;q=x", false},
		{"br", false},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Accept-Encoding", tt.header)
			if got := serve(middleware.Compress()(ok), r).Header().Get("Content-Encoding") == "gzip"; got != tt.gzip {
				t.Fatalf("gzip %v, want %v", got, tt.gzip)
			}
		})
	}
}

func TestCORSOriginFuncAndAnyOrigin(t *testing.T) {
	tests := []struct {
		name    string
		opts    middleware.CORSOptions
		origin  string
		want    string
		headers string
	}{
		{"func allows", middleware.CORSOptions{AllowOriginFunc: func(o string) bool { return strings.HasSuffix(o, ".app.io") }}, "https://a.app.io", "https://a.app.io", ""},
		{"func refuses", middleware.CORSOptions{AllowOriginFunc: func(string) bool { return false }}, "https://a.app.io", "", ""},
		{"any origin", middleware.CORSOptions{AllowOrigins: []string{"*"}, AllowHeaders: []string{"Content-Type"}}, "https://x.io", "*", "Content-Type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("OPTIONS", "/", nil)
			r.Header.Set("Origin", tt.origin)
			r.Header.Set("Access-Control-Request-Method", "PUT")
			w := serve(middleware.CORS(tt.opts)(ok), r)
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != tt.want {
				t.Fatalf("Allow-Origin %q, want %q", got, tt.want)
			}
			if got := w.Header().Get("Access-Control-Allow-Headers"); got != tt.headers {
				t.Fatalf("Allow-Headers %q, want %q", got, tt.headers)
			}
		})
	}
}

func TestCompressInformationalStatus(t *testing.T) {
	big := strings.Repeat("x", 4096)
	srv := httptest.NewServer(middleware.Compress()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", "</app.css>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		_, _ = io.WriteString(w, big)
	})))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := srv.Client().Do(req) // the transport asks for gzip and decodes it
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !resp.Uncompressed || string(b) != big {
		t.Fatalf("status %d, uncompressed %v, %d bytes", resp.StatusCode, resp.Uncompressed, len(b))
	}
}
