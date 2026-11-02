package dygo

import (
	"net/http"

	"github.com/dygoframework/dygo/errs"
)

const defaultBodyLimit = 1 << 20 // 1 MiB

// Middleware is standard net/http middleware, so any existing Go middleware works.
type Middleware = func(http.Handler) http.Handler

type httpOptions struct {
	globalPrefix  string
	prefixExclude []string
	versioning    *VersioningOptions
	middleware    []Middleware
	serverFns     []func(*http.Server)
	bodyLimit     int64
	errorWriter   errs.Writer
	global        [kindCount][]any // WithGlobal* enhancer arguments
}

// WithGlobalPrefix puts prefix in front of every route. Routes whose path
// (without the prefix) equals or starts with one of exclude are left alone,
// for example "/health".
func WithGlobalPrefix(prefix string, exclude ...string) Option {
	return func(o *options) {
		o.http.globalPrefix = prefix
		o.http.prefixExclude = exclude
	}
}

// WithVersioning turns on API versioning.
func WithVersioning(v VersioningOptions) Option {
	return func(o *options) { o.http.versioning = &v }
}

// WithMiddleware adds application wide middleware. It wraps everything,
// including 404 responses. The first middleware is the outermost.
func WithMiddleware(mw ...Middleware) Option {
	return func(o *options) { o.http.middleware = append(o.http.middleware, mw...) }
}

// WithHTTPServer lets you change the *http.Server before it starts: timeouts,
// TLS, error log. Defaults: ReadHeaderTimeout 10s, IdleTimeout 120s.
func WithHTTPServer(fn func(*http.Server)) Option {
	return func(o *options) { o.http.serverFns = append(o.http.serverFns, fn) }
}

// WithBodyLimit sets the maximum request body size in bytes. The default is
// 1 MiB; zero or less means no limit. Routes can override it with [BodyLimit].
func WithBodyLimit(n int64) Option {
	return func(o *options) { o.http.bodyLimit = n }
}

// WithErrorWriter replaces the default error writer ([errs.DefaultWriter]).
func WithErrorWriter(w errs.Writer) Option {
	return func(o *options) { o.http.errorWriter = w }
}
