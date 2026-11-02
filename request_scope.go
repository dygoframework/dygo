package dygo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
)

// OnRequestEnd is called on every request scoped instance when the request
// that created it finishes, in reverse creation order. Use it for per-request
// cleanup such as committing or rolling back a transaction.
type OnRequestEnd interface {
	OnRequestEnd(ctx context.Context)
}

// requestScope holds the request scoped instances of one request.
type requestScope struct {
	ctx       context.Context
	req       *http.Request
	mu        sync.Mutex
	instances map[*node]reflect.Value
	order     []reflect.Value

	cid       ContextID     // from the strategy or WithDurableContext
	noRequest bool          // a WithDurableContext scope outside a request
	durable   *durableScope // set when this is a durable scope
}

// scopeFrom returns the request scope of the request handled with ctx, or nil
// outside a request.
func scopeFrom(ctx context.Context) *requestScope {
	if ctx == nil {
		return nil
	}
	if cid, ok := ctx.Value(durableKey{}).(ContextID); ok {
		return &requestScope{ctx: ctx, cid: cid, noRequest: true}
	}
	if ls, ok := ctx.Value(scopeKey{}).(*lazyScope); ok {
		return ls.get()
	}
	if st := stateFrom(ctx); st != nil {
		return st.requestScope()
	}
	return nil
}

type scopeKey struct{}

// lazyScope is a request scope opened with [App.WithRequestScope]. Like the
// scope of an HTTP request it is created on first use, so a unit of work
// that never resolves a request scoped provider pays only for the context.
type lazyScope struct {
	once  sync.Once
	rs    *requestScope
	c     *container
	ctx   context.Context
	req   *http.Request
	ended atomic.Bool
}

// get returns the scope, nil once it has ended, so a goroutine that outlives
// the work gets an error instead of instances already cleaned up.
func (ls *lazyScope) get() *requestScope {
	if ls.ended.Load() {
		return nil
	}
	ls.once.Do(func() {
		ls.rs = &requestScope{ctx: ls.ctx, req: ls.req, instances: map[*node]reflect.Value{}}
		if ls.req != nil && ls.c.strategy != nil {
			ls.rs.cid.ID, ls.rs.cid.Payload = ls.c.strategy(ls.req)
		}
	})
	return ls.rs
}

// WithRequestScope returns ctx carrying a new request scope, for a transport
// whose unit of work is not an HTTP request, such as a WebSocket message:
// [Scoped.Get] with the returned context builds request scoped providers
// once for that unit, and end calls their OnRequestEnd hooks. Call end when
// the work is done, from the goroutine that did it. r is the request the
// work belongs to (the WebSocket handshake, for example), or nil; it is
// what request scoped providers get for *http.Request and what the
// [ContextIDStrategy] reads, so durable providers resolve for its context
// ID. The new scope takes precedence over the scope of a request ctx
// already belongs to; [WithDurableContext] still takes precedence over both.
func (a *App) WithRequestScope(ctx context.Context, r *http.Request) (_ context.Context, end func()) {
	ls := &lazyScope{c: a.c, req: r}
	ls.ctx = context.WithValue(ctx, scopeKey{}, ls)
	return ls.ctx, func() {
		if ls.ended.Swap(true) {
			return
		}
		ls.once.Do(func() {}) // waits for a scope being created
		if ls.rs != nil {
			ls.rs.end()
		}
	}
}

var errNoRequest = errors.New("no request in context")

// ensure returns n's instance for this request, building it on first use.
// Construction happens outside the lock so that dependencies can be resolved
// recursively; if two goroutines of one request race, the first result wins.
func (rs *requestScope) ensure(c *container, n *node) (reflect.Value, error) {
	if rs == nil {
		return reflect.Value{}, fmt.Errorf("module %q: request scoped %s used outside a request: %w", n.mod.name, n, errNoRequest)
	}
	rs.mu.Lock()
	v, ok := rs.instances[n]
	rs.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := c.construct(rs, n)
	if err != nil {
		return reflect.Value{}, err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if prev, ok := rs.instances[n]; ok {
		return prev, nil
	}
	rs.instances[n] = v
	rs.order = append(rs.order, v)
	return v, nil
}

// end calls OnRequestEnd on the request's instances, newest first.
func (rs *requestScope) end() {
	rs.mu.Lock()
	order := rs.order
	rs.mu.Unlock()
	for i := len(order) - 1; i >= 0; i-- {
		if h, ok := order[i].Interface().(OnRequestEnd); ok {
			h.OnRequestEnd(rs.ctx)
		}
	}
}
