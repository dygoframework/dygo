// Package ratelimit limits requests per client:
//
//	app, err := dygo.Create(ctx, AppModule,
//		dygo.WithGlobalGuards(ratelimit.Guard(ratelimit.Options{Limit: 100, Window: time.Minute})))
//
//	r.Post("/login", dygo.Handle(c.Login), ratelimit.Limit(5, time.Minute)) // @Throttle
//	r.Get("/health", h, ratelimit.Skip())                                    // @SkipThrottle
//
// Each route has its own counter per client. The MemoryStore
// uses GCRA (a token bucket that stores one timestamp per client), so a
// client cannot burst to twice the limit across a window boundary as with a
// fixed window counter. Responses carry
// X-RateLimit-Limit, X-RateLimit-Remaining and X-RateLimit-Reset; a blocked
// request gets 429 with Retry-After.
package ratelimit

import (
	"context"
	"hash/maphash"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
)

// Result is the outcome of one Take.
type Result struct {
	Allowed   bool
	Remaining int
	// Reset is how long until the window resets.
	Reset time.Duration
}

// Store counts requests. MemoryStore works for one instance; implement Store
// over Redis or similar to share limits between instances.
type Store interface {
	Take(ctx context.Context, key string, limit int, window time.Duration) (Result, error)
}

// Options configure Guard.
type Options struct {
	// Limit requests per Window. Defaults: 100 per minute.
	Limit  int
	Window time.Duration
	// Key identifies the client. Defaults to the client IP.
	Key func(ec dygo.ExecutionContext) string
	// TrustProxy makes the default Key read the client IP from
	// X-Forwarded-For, as added by your proxy. Enable it only behind a proxy
	// that sets the header.
	TrustProxy bool
	// ProxyHops is the number of proxies in front of the app that append to
	// X-Forwarded-For (a load balancer and an ingress make 2). Defaults to 1.
	ProxyHops int
	// Store defaults to a new MemoryStore.
	Store Store
}

type (
	limitKey struct{}
	skipKey  struct{}
)

type limit struct {
	n      int
	window time.Duration
}

// Limit overrides the limit for a route or, through Router.Use, a controller.
func Limit(n int, window time.Duration) dygo.RouteOption {
	return dygo.SetMetadata(limitKey{}, limit{n, window})
}

// Skip turns rate limiting off for a route or controller.
func Skip() dygo.RouteOption { return dygo.SetMetadata(skipKey{}, true) }

// Guard returns the rate limiting guard. If the store fails, the request is
// allowed: an outage of the counter store should not take the API down.
func Guard(o Options) dygo.Guard {
	if o.Limit <= 0 {
		o.Limit = 100
	}
	if o.Window <= 0 {
		o.Window = time.Minute
	}
	if o.Store == nil {
		o.Store = NewMemoryStore()
	}
	if o.Key == nil {
		hops := 0
		if o.TrustProxy {
			hops = max(o.ProxyHops, 1)
		}
		o.Key = func(ec dygo.ExecutionContext) string { return ClientIP(ec.HTTP().Request(), hops) }
	}
	return dygo.GuardFunc(func(ec dygo.ExecutionContext) (bool, error) {
		if skip, _ := ec.MergedMetadata(skipKey{}); skip == true {
			return true, nil
		}
		l := limit{o.Limit, o.Window}
		if v, ok := ec.MergedMetadata(limitKey{}); ok {
			l = v.(limit)
		}
		ri := ec.Route()
		res, err := o.Store.Take(ec.Context(), ri.Method+" "+ri.Host+ri.Path+" "+o.Key(ec), l.n, l.window)
		if err != nil {
			return true, nil //nolint:nilerr // fail open, see the doc comment
		}
		h := ec.HTTP().Response().Header()
		h.Set("X-RateLimit-Limit", strconv.Itoa(l.n))
		h.Set("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
		reset := strconv.Itoa(int((res.Reset + time.Second - 1) / time.Second))
		h.Set("X-RateLimit-Reset", reset)
		if !res.Allowed {
			h.Set("Retry-After", reset)
			return false, errs.TooManyRequests("", errs.Code("RATE_LIMITED"))
		}
		return true, nil
	})
}

// ClientIP returns the request's client address without the port. With
// hops > 0 it reads X-Forwarded-For instead, counting hops entries from the
// right: those were added by your own proxies, while entries further left
// come from the client and can be forged.
func ClientIP(r *http.Request, hops int) string {
	if hops > 0 {
		var all []string
		for _, v := range r.Header.Values("X-Forwarded-For") {
			for p := range strings.SplitSeq(v, ",") {
				all = append(all, strings.TrimSpace(p))
			}
		}
		if len(all) >= hops {
			return all[len(all)-hops]
		}
		if len(all) > 0 {
			return all[0]
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// MemoryStore is an in-process GCRA limiter, sharded to keep lock contention
// low. It keeps one timestamp per client and route; entries whose bucket is
// full again are swept as the store is used.
type MemoryStore struct {
	seed   maphash.Seed
	shards [32]shard
	now    func() time.Time
}

type shard struct {
	mu  sync.Mutex
	tat map[string]int64 // theoretical arrival time, unix nanoseconds
	ops int
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{seed: maphash.MakeSeed(), now: time.Now}
	for i := range s.shards {
		s.shards[i].tat = map[string]int64{}
	}
	return s
}

// Take counts one request for key: limit requests per window, refilling
// evenly (one every window/limit).
func (s *MemoryStore) Take(_ context.Context, key string, limit int, window time.Duration) (Result, error) {
	if limit <= 0 {
		return Result{Reset: window}, nil
	}
	interval := int64(window) / int64(limit)
	sh := &s.shards[maphash.String(s.seed, key)%uint64(len(s.shards))]
	now := s.now().UnixNano()
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.ops++; sh.ops >= 1024 {
		sh.ops = 0
		for k, t := range sh.tat {
			if t <= now {
				delete(sh.tat, k)
			}
		}
	}
	tat := max(sh.tat[key], now)
	next := tat + interval
	if allowAt := next - int64(window); now < allowAt {
		return Result{Allowed: false, Remaining: 0, Reset: time.Duration(allowAt - now)}, nil
	}
	sh.tat[key] = next
	remaining := int((int64(window) - (next - now)) / interval)
	return Result{Allowed: true, Remaining: max(remaining, 0), Reset: time.Duration(next - now)}, nil
}
