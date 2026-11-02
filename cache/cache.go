// Package cache is a key-value cache with a time to live: a [Store] (in
// memory by default), typed helpers, an HTTP response cache interceptor and
// [Wrap] to cache a function.
//
//	dygo.Module{Imports: []*dygo.Module{cache.Module(cache.Options{TTL: time.Minute})}}
//
//	u, err := cache.GetOrSet(ctx, c, "user:"+id, 0, func(ctx context.Context) (*User, error) {
//		return s.repo.Find(ctx, id)
//	})
//
//	r.Get("/products", dygo.Handle(c.List), cache.TTL(30*time.Second))
//	r = r.Use(dygo.Interceptors(dygo.Ref[*cache.Interceptor]()))
//
// Values are stored as JSON, so every store (memory, Redis) behaves the same.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dygoframework/dygo"
)

// Store keeps raw values. Implementations must be safe for concurrent use.
// Get returns ok false for a missing or expired key.
type Store interface {
	Get(ctx context.Context, key string) (val []byte, ok bool, err error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// Options configures [Module] and [New].
type Options struct {
	// Store holds the values. The default is NewMemory(10_000).
	Store Store
	// TTL is the default time to live; 0 means entries do not expire.
	TTL time.Duration
	// Prefix is put in front of every key, so apps can share a store.
	Prefix string
}

// Cache reads and writes JSON values in a Store.
type Cache struct {
	store  Store
	ttl    time.Duration
	prefix string

	mu       sync.Mutex
	inflight map[string]*call
}

type call struct {
	done chan struct{}
	val  []byte
	err  error
}

// New returns a cache.
func New(o Options) *Cache {
	if o.Store == nil {
		o.Store = NewMemory(10_000)
	}
	return &Cache{store: o.Store, ttl: o.TTL, prefix: o.Prefix, inflight: map[string]*call{}}
}

// Module provides *Cache and the HTTP *Interceptor. It is global; import it
// once in the root module.
func Module(o Options) *dygo.Module {
	return &dygo.Module{
		Name:   "cache",
		Global: true,
		Providers: []dygo.Provider{
			dygo.Provide(func() *Cache { return New(o) }),
			dygo.Provide(NewInterceptor),
		},
		Exports: []dygo.Token{dygo.TypeOf[*Cache](), dygo.TypeOf[*Interceptor]()},
	}
}

// TTL is the default time to live.
func (c *Cache) TTL() time.Duration { return c.ttl }

func (c *Cache) ttlOr(ttl time.Duration) time.Duration {
	if ttl == 0 {
		return c.ttl
	}
	return ttl
}

// GetRaw returns the stored bytes of key.
func (c *Cache) GetRaw(ctx context.Context, key string) ([]byte, bool, error) {
	return c.store.Get(ctx, c.prefix+key)
}

// SetRaw stores bytes under key. ttl 0 means the default TTL.
func (c *Cache) SetRaw(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return c.store.Set(ctx, c.prefix+key, val, c.ttlOr(ttl))
}

// Delete removes key.
func (c *Cache) Delete(ctx context.Context, key string) error {
	return c.store.Delete(ctx, c.prefix+key)
}

// Get decodes the value of key into a T.
func Get[T any](ctx context.Context, c *Cache, key string) (T, bool, error) {
	var v T
	b, ok, err := c.GetRaw(ctx, key)
	if err != nil || !ok {
		return v, false, err
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, false, fmt.Errorf("cache: decoding %q: %w", key, err)
	}
	return v, true, nil
}

// Set stores v as JSON under key. ttl 0 means the default TTL.
func Set(ctx context.Context, c *Cache, key string, v any, ttl time.Duration) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("cache: encoding %q: %w", key, err)
	}
	return c.SetRaw(ctx, key, b, ttl)
}

// GetOrSet returns the cached value of key, or calls fn, stores its result
// and returns it. Concurrent callers for the same missing key share one fn
// call. An error from fn is returned and not cached.
func GetOrSet[T any](ctx context.Context, c *Cache, key string, ttl time.Duration, fn func(ctx context.Context) (T, error)) (T, error) {
	var v T
	if got, ok, err := Get[T](ctx, c, key); err == nil && ok {
		return got, nil
	}
	b, err := c.once(ctx, key, func() ([]byte, error) {
		v, err := fn(ctx)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("cache: encoding %q: %w", key, err)
		}
		if err := c.SetRaw(ctx, key, b, ttl); err != nil {
			return nil, err
		}
		return b, nil
	})
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}

// once runs fn for key unless a call for key is running, in which case it
// waits for that call's result.
func (c *Cache) once(ctx context.Context, key string, fn func() ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	if cl, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-cl.done:
			return cl.val, cl.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	cl := &call{done: make(chan struct{})}
	c.inflight[key] = cl
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.inflight, key)
		c.mu.Unlock()
		close(cl.done)
	}()
	cl.val, cl.err = fn()
	if cl.err == nil && cl.val == nil {
		cl.err = errors.New("cache: no value")
	}
	return cl.val, cl.err
}

// Wrap returns fn with its results cached under prefix + ":" + the argument
// (formatted with %v), for ttl (0 is the default TTL). Go cannot implement an
// interface at runtime, so caching a service means a small decorator that
// keeps the wrapped functions:
//
//	type cachedUsers struct {
//		Users
//		byID func(context.Context, string) (*User, error)
//	}
//
//	func (u cachedUsers) ByID(ctx context.Context, id string) (*User, error) { return u.byID(ctx, id) }
//
//	func NewCachedUsers(u *UserService, c *cache.Cache) Users {
//		return cachedUsers{Users: u, byID: cache.Wrap(c, "user", time.Minute, u.ByID)}
//	}
func Wrap[K comparable, V any](c *Cache, prefix string, ttl time.Duration, fn func(ctx context.Context, k K) (V, error)) func(ctx context.Context, k K) (V, error) {
	return func(ctx context.Context, k K) (V, error) {
		return GetOrSet(ctx, c, fmt.Sprintf("%s:%v", prefix, k), ttl, func(ctx context.Context) (V, error) {
			return fn(ctx, k)
		})
	}
}
