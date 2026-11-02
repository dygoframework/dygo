package dygo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dygoframework/dygo/errs"
)

// Responder is an Out value that writes its own response.
type Responder interface {
	Respond(w http.ResponseWriter, r *http.Request) error
}

// RawJSON as Out is a body already encoded as JSON. It is written as is,
// with the route's status and a JSON content type, and is not checked, so it
// must come from json.Marshal or another trusted encoder. The cache
// interceptor uses it to answer from stored bytes.
type RawJSON []byte

// NoContent as Out writes 204 with no body.
type NoContent struct{}

var newline = []byte{'\n'} // json.Encoder ends every value with one

var jsonBufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// writeOut writes a handler result.
func writeOut(st *reqState, rc *routeConfig, out any) error {
	h := st.w.Header()
	for _, kv := range rc.headers {
		h.Set(kv[0], kv[1])
	}
	if rc.redirectURL != "" {
		http.Redirect(st.w, st.request(), rc.redirectURL, rc.redirectCode)
		return nil
	}
	status := st.status
	if status == 0 {
		status = rc.code
	}
	if status == 0 {
		status = http.StatusOK
		if st.r.Method == http.MethodPost {
			status = http.StatusCreated
		}
	}
	switch v := out.(type) {
	case Responder:
		return v.Respond(st.w, st.request())
	case NoContent, *NoContent:
		st.w.WriteHeader(http.StatusNoContent)
		return nil
	case []byte:
		if h.Get("Content-Type") == "" {
			h.Set("Content-Type", "application/octet-stream")
		}
		st.w.WriteHeader(status)
		_, err := st.w.Write(v)
		return err
	case string:
		if h.Get("Content-Type") == "" {
			h.Set("Content-Type", "text/plain; charset=utf-8")
		}
		st.w.WriteHeader(status)
		_, err := io.WriteString(st.w, v)
		return err
	case RawJSON:
		h.Set("Content-Type", "application/json; charset=utf-8")
		st.w.WriteHeader(status)
		if _, err := st.w.Write(v); err != nil {
			return err
		}
		_, err := st.w.Write(newline)
		return err
	default:
		buf := jsonBufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer func() {
			if buf.Cap() <= 64<<10 { // do not keep huge buffers alive
				jsonBufPool.Put(buf)
			}
		}()
		if err := json.NewEncoder(buf).Encode(out); err != nil {
			return fmt.Errorf("encoding response: %w", err)
		}
		h.Set("Content-Type", "application/json; charset=utf-8")
		st.w.WriteHeader(status)
		_, err := st.w.Write(buf.Bytes())
		return err
	}
}

type responderFunc func(w http.ResponseWriter, r *http.Request) error

func (f responderFunc) Respond(w http.ResponseWriter, r *http.Request) error { return f(w, r) }

// Stream writes r as the body with the given content type. r is closed
// afterwards if it is an io.Closer.
func Stream(r io.Reader, contentType string) Responder {
	return responderFunc(func(w http.ResponseWriter, _ *http.Request) error {
		if c, ok := r.(io.Closer); ok {
			defer func() { _ = c.Close() }()
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, err := io.Copy(w, r)
		return err
	})
}

// File serves a file from disk with http.ServeContent, which handles Range,
// If-Modified-Since and content type detection. path must not come from user
// input without cleaning.
func File(path string) Responder {
	return responderFunc(func(w http.ResponseWriter, r *http.Request) error {
		f, err := os.Open(path) //nolint:gosec // G304: the handler picks path; the doc comment says to clean user input
		if err != nil {
			if os.IsNotExist(err) {
				return errs.NotFound("file not found")
			}
			return err
		}
		defer func() { _ = f.Close() }()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return errs.NotFound("file not found")
		}
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
		return nil
	})
}

// RedirectTo redirects to url with code (for example 302 or 307). Return it
// from a handler for a dynamic target; for a fixed one, use the [Redirect]
// route option.
func RedirectTo(url string, code int) Responder {
	return responderFunc(func(w http.ResponseWriter, r *http.Request) error {
		http.Redirect(w, r, url, code)
		return nil
	})
}

// SSEEvent is a server-sent event with optional id, event name and retry.
// Yield it from an [SSE] sequence to control those fields; any other value is
// sent as a data-only event.
type SSEEvent struct {
	ID    string
	Event string
	Data  any
	Retry time.Duration
}

// SSE streams events as text/event-stream. Each value is
// JSON encoded (strings are sent as is). The stream ends when the sequence
// ends, the client disconnects, or the server shuts down. The sequence itself
// should also watch the request context if it can block for a long time.
func SSE[T any](events iter.Seq[T]) Responder {
	return responderFunc(func(w http.ResponseWriter, r *http.Request) error {
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_ = rc.Flush()
		stopping := Stopping(r.Context())
		for ev := range events {
			select {
			case <-r.Context().Done():
				return nil
			case <-stopping:
				return nil
			default:
			}
			if err := writeSSE(w, ev); err != nil {
				return err
			}
			if err := rc.Flush(); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeSSE(w io.Writer, v any) error {
	ev, ok := v.(SSEEvent)
	if !ok {
		ev = SSEEvent{Data: v}
	}
	var b strings.Builder
	if ev.ID != "" {
		fmt.Fprintf(&b, "id: %s\n", ev.ID)
	}
	if ev.Event != "" {
		fmt.Fprintf(&b, "event: %s\n", ev.Event)
	}
	if ev.Retry > 0 {
		fmt.Fprintf(&b, "retry: %d\n", ev.Retry.Milliseconds())
	}
	var data string
	if s, ok := ev.Data.(string); ok {
		data = s
	} else {
		j, err := json.Marshal(ev.Data)
		if err != nil {
			return err
		}
		data = string(j)
	}
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteString("\n")
	_, err := io.WriteString(w, b.String())
	return err
}
