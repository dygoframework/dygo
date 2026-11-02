// Package middleware holds net/http middleware for production services:
// request IDs, request logging, CORS, security headers, CSRF protection and
// compression. Each is a plain func(http.Handler) http.Handler, so it works
// with dygo.WithMiddleware, route middleware, or any Go router.
//
//	app, err := dygo.Create(ctx, AppModule, dygo.WithMiddleware(
//		middleware.RequestID(),
//		middleware.Logger(log),
//		middleware.SecureHeaders(middleware.SecureHeadersOptions{}),
//		middleware.CORS(middleware.CORSOptions{AllowOrigins: []string{"https://app.example.com"}}),
//		middleware.Compress(),
//	))
package middleware

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// RequestIDHeader is the header read and written by RequestID.
const RequestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// RequestID reuses the client's X-Request-ID when it looks sane (at most 128
// printable characters), otherwise generates one. It echoes it in the
// response and stores it in the context for RequestIDFrom.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if !validID(id) {
				id = newID()
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
		})
	}
}

// RequestIDFrom returns the request ID set by RequestID, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func validID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := range len(id) {
		if id[i] < 0x21 || id[i] > 0x7e {
			return false
		}
	}
	return true
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Logger logs one line per request with method, path, status, bytes,
// duration_ms and the request ID: Info, or Error for 5xx responses.
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			if status >= 500 {
				level = slog.LevelError
			}
			ctx := r.Context()
			if !log.Enabled(ctx, level) {
				return
			}
			attrs := [6]slog.Attr{
				slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Int("status", status),
				slog.Int64("bytes", rec.bytes), slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
			}
			n := 5
			if id := RequestIDFrom(ctx); id != "" {
				attrs[n] = slog.String("request_id", id)
				n++
			}
			log.LogAttrs(ctx, level, "request", attrs[:n]...)
		})
	}
}

// recorder captures the status and size while keeping Flush, Hijack and
// Unwrap working, so SSE and WebSockets pass through.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// ReadFrom keeps sendfile working for file responses (io.Copy to the writer).
func (r *recorder) ReadFrom(src io.Reader) (int64, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	var n int64
	var err error
	if rf, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(src)
	} else {
		n, err = io.Copy(writerOnly{r.ResponseWriter}, src)
	}
	r.bytes += n
	return n, err
}

// writerOnly hides ReadFrom so io.Copy does not loop back into it.
type writerOnly struct{ io.Writer }

func (r *recorder) Flush() { _ = http.NewResponseController(r.ResponseWriter).Flush() }

func (r *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(r.ResponseWriter).Hijack()
}
