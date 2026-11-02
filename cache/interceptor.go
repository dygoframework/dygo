package cache

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/meta"
)

var (
	ttlKey  = meta.NewKey[time.Duration]("cache.ttl")
	keyKey  = meta.NewKey[string]("cache.key")
	skipKey = meta.NewKey[bool]("cache.skip")
)

// TTL sets the time to live of a route's cached responses, like @CacheTTL.
func TTL(d time.Duration) dygo.RouteOption { return ttlKey.Set(d) }

// Key sets a fixed cache key for a route, like @CacheKey. By default the key
// is the request path with its query.
func Key(k string) dygo.RouteOption { return keyKey.Set(k) }

// NoCache turns the interceptor off for a route.
func NoCache() dygo.RouteOption { return skipKey.Set(true) }

// Interceptor caches the results of GET routes.
// It caches JSON results, strings and byte slices, not Responders (streams,
// files, SSE) or NoContent; headers set by the handler are not cached. Other
// methods and transports pass through. A JSON result is encoded once: the
// interceptor returns it as dygo.RawJSON, on a hit and on a miss, so
// interceptors outside it see the encoded bytes.
type Interceptor struct{ c *Cache }

// NewInterceptor returns the interceptor for c.
func NewInterceptor(c *Cache) *Interceptor { return &Interceptor{c: c} }

// Kinds of cached result, the first byte of a cached value.
const (
	kindJSON  = 'j'
	kindText  = 's'
	kindBytes = 'b'
)

// Intercept implements dygo.Interceptor.
func (i *Interceptor) Intercept(ec dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
	host := ec.HTTP()
	if host == nil {
		return next.Handle()
	}
	r := host.Request()
	if r.Method != http.MethodGet {
		return next.Handle()
	}
	if skip, _ := skipKey.Get(ec); skip {
		return next.Handle()
	}
	key, ok := keyKey.Get(ec)
	if !ok {
		key = "http:" + r.URL.RequestURI()
	}
	ctx := ec.Context()
	if b, ok, err := i.c.GetRaw(ctx, key); err == nil && ok && len(b) > 0 {
		switch b[0] {
		case kindJSON:
			return dygo.RawJSON(b[1:]), nil
		case kindText:
			return string(b[1:]), nil
		case kindBytes:
			return b[1:], nil
		}
	}
	out, err := next.Handle()
	if err != nil {
		return out, err
	}
	var val []byte
	switch v := out.(type) {
	case dygo.Responder, dygo.NoContent, *dygo.NoContent, nil:
		return out, nil
	case string:
		val = append([]byte{kindText}, v...)
	case []byte:
		val = append([]byte{kindBytes}, v...)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return out, nil // the response writer reports it
		}
		val = append([]byte{kindJSON}, b...)
	}
	ttl, _ := ttlKey.Get(ec)
	_ = i.c.SetRaw(ctx, key, val, ttl) // a failing store must not fail the request
	if val[0] == kindJSON {
		return dygo.RawJSON(val[1:]), nil // already encoded, so it is not encoded twice
	}
	return out, nil
}
