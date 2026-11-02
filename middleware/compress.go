package middleware

import (
	"bufio"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// DefaultCompressMinSize is the smallest body Compress gzips. Below it, gzip
// costs more CPU (resetting the compressor dominates) than it saves on the
// wire, and the result can even be larger.
const DefaultCompressMinSize = 1024

var gzipPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
	return w
}}

var gzipWriterPool = sync.Pool{New: func() any { return &gzipWriter{} }}

// Compress gzips responses for clients that accept it. It holds back the first
// DefaultCompressMinSize bytes, and sends smaller bodies as they are. It leaves
// alone responses that are already encoded, server-sent event streams, and
// common already compressed types (images, video, archives). Flush sends what
// is buffered at once, so streaming responses keep streaming.
func Compress() func(http.Handler) http.Handler {
	return CompressMin(DefaultCompressMinSize)
}

// CompressMin is Compress with a different minimum body size.
func CompressMin(minSize int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			addVary(w.Header(), varyAcceptEncoding)
			if r.Method == http.MethodHead || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
				next.ServeHTTP(w, r)
				return
			}
			cw := gzipWriterPool.Get().(*gzipWriter)
			cw.ResponseWriter, cw.min = w, minSize
			defer func() {
				cw.close()
				*cw = gzipWriter{buf: cw.buf[:0]}
				gzipWriterPool.Put(cw)
			}()
			next.ServeHTTP(cw, r)
		})
	}
}

func acceptsGzip(ae string) bool {
	for ae != "" {
		var part string
		part, ae, _ = strings.Cut(ae, ",")
		name, params, _ := strings.Cut(part, ";")
		if n := strings.TrimSpace(name); n != "gzip" && n != "*" {
			continue
		}
		_, q, ok := strings.Cut(params, "q=")
		if !ok {
			return true
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(q), 64)
		return err == nil && f > 0
	}
	return false
}

// gzipWriter buffers the start of the body until it knows whether the
// response is worth compressing.
type gzipWriter struct {
	http.ResponseWriter
	min     int
	status  int    // held back until the decision
	buf     []byte // held back body, at most min bytes
	decided bool
	gz      *gzip.Writer
	hijack  bool
}

// decide sends the held back header, then switches to gzip or passthrough.
// compress is false when the body turned out small.
func (w *gzipWriter) decide(compress bool) {
	w.decided = true
	if w.status == 0 {
		w.status = http.StatusOK
	}
	h := w.Header()
	if compress && w.status >= 200 && w.status != http.StatusNoContent && w.status != http.StatusNotModified &&
		h.Get("Content-Encoding") == "" && !skipType(h.Get("Content-Type")) {
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		w.gz = gzipPool.Get().(*gzip.Writer)
		w.gz.Reset(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(w.status)
	if len(w.buf) > 0 {
		if w.gz != nil {
			_, _ = w.gz.Write(w.buf)
		} else {
			_, _ = w.ResponseWriter.Write(w.buf)
		}
		w.buf = w.buf[:0]
	}
}

// skipPrefixes are content types Compress leaves alone: streams that must not
// be buffered, and formats that are already compressed.
var skipPrefixes = []string{
	"text/event-stream", "image/", "video/", "audio/", "font/woff",
	"application/zip", "application/gzip", "application/x-gzip", "application/zstd",
}

func skipType(ct string) bool {
	ct = strings.ToLower(ct)
	for _, p := range skipPrefixes {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return false
}

func (w *gzipWriter) WriteHeader(code int) {
	if w.decided || w.status != 0 {
		return
	}
	if code < 200 { // 1xx informational responses go straight through
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.status = code
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if !w.decided {
		if w.status == 0 {
			w.status = http.StatusOK
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(b))
		}
		if len(w.buf)+len(b) < w.min {
			w.buf = append(w.buf, b...)
			return len(b), nil
		}
		w.decide(true)
	}
	if w.gz == nil {
		return w.ResponseWriter.Write(b)
	}
	return w.gz.Write(b)
}

// Flush ends buffering: a streaming response is compressed if eligible.
func (w *gzipWriter) Flush() {
	if !w.decided {
		w.decide(true)
	}
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *gzipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijack = true
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gzipWriter) close() {
	if w.hijack {
		return
	}
	if !w.decided {
		if w.status == 0 && len(w.buf) == 0 {
			return // the handler wrote nothing; net/http sends its default 200
		}
		w.decide(false) // everything fit under the minimum
	}
	if w.gz == nil {
		return
	}
	_ = w.gz.Close()
	w.gz.Reset(io.Discard)
	gzipPool.Put(w.gz)
	w.gz = nil
}
