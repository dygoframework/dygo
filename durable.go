package dygo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
)

// Durable providers, a request scope shared per tenant for multi-tenant
// apps. A [ContextIDStrategy] maps each request to a context ID, such as the
// tenant; providers marked [Durable] are then built once per context ID and
// shared by every request that has it, while plain request scoped providers
// stay per request.

// ContextID is the durable context a durable provider is built for. Durable
// providers inject it to learn their tenant:
//
//	func NewTenantDB(cid dygo.ContextID, pools *Pools) (*TenantDB, error) {
//		return pools.Open(cid.ID)
//	}
//
// In a plain request scoped provider it holds the request's ID and payload,
// or nothing when there is no strategy.
type ContextID struct {
	// ID is the context ID returned by the strategy, "" for none.
	ID string
	// Payload is the strategy's payload from the first request with this
	// ID.
	Payload any
}

// ContextIDStrategy returns the context ID of a request. An empty id means
// the request has none: durable providers then behave like plain request
// scoped ones for it.
type ContextIDStrategy func(r *http.Request) (id string, payload any)

// WithContextIDStrategy sets how requests map to durable contexts:
//
//	dygo.WithContextIDStrategy(func(r *http.Request) (string, any) {
//		t := r.Header.Get("X-Tenant-ID")
//		return t, t
//	})
//
// The ID is trusted as is: validate it (for example against known tenants)
// in the strategy, since every new ID builds and keeps new instances.
func WithContextIDStrategy(s ContextIDStrategy) Option {
	return func(o *options) { o.contextIDStrategy = s }
}

type durableKey struct{}

// WithDurableContext returns ctx carrying a context ID, so that
// [Scoped.Get] resolves durable providers for it outside an HTTP request:
// in a queue job, an event listener or a microservice handler. Plain
// request scoped providers cannot be resolved with it. Inside a request it
// takes precedence over the strategy.
func WithDurableContext(ctx context.Context, id string, payload any) context.Context {
	return context.WithValue(ctx, durableKey{}, ContextID{ID: id, Payload: payload})
}

// durableScopes holds one scope per context ID.
type durableScopes struct {
	mu     sync.Mutex
	scopes map[string]*durableScope
}

// durableScope holds the durable instances of one context ID. Unlike a
// request scope, each instance is built exactly once even when requests
// race, because durable providers often own resources such as a pool.
type durableScope struct {
	rs    *requestScope
	mu    sync.Mutex
	cells map[*node]*durableCell
}

type durableCell struct {
	ready chan struct{}
	v     reflect.Value
	err   error
}

// durable returns the scope of cid, creating it on first use.
func (c *container) durable(cid ContextID) *durableScope {
	c.durables.mu.Lock()
	defer c.durables.mu.Unlock()
	if ds := c.durables.scopes[cid.ID]; ds != nil {
		return ds
	}
	if c.durables.scopes == nil {
		c.durables.scopes = map[string]*durableScope{}
	}
	ds := &durableScope{
		rs:    &requestScope{ctx: c.ctx, cid: cid, instances: map[*node]reflect.Value{}},
		cells: map[*node]*durableCell{},
	}
	ds.rs.durable = ds
	c.durables.scopes[cid.ID] = ds
	return ds
}

func (ds *durableScope) ensure(c *container, n *node) (reflect.Value, error) {
	ds.mu.Lock()
	cell, ok := ds.cells[n]
	if !ok {
		cell = &durableCell{ready: make(chan struct{})}
		ds.cells[n] = cell
	}
	ds.mu.Unlock()
	if ok {
		<-cell.ready
		return cell.v, cell.err
	}
	cell.v, cell.err = c.construct(ds.rs, n)
	ds.mu.Lock()
	if cell.err != nil {
		delete(ds.cells, n) // a later request tries again
	} else {
		ds.rs.order = append(ds.rs.order, cell.v)
	}
	ds.mu.Unlock()
	close(cell.ready)
	return cell.v, cell.err
}

// shutdown calls OnApplicationShutdown on the scope's instances, newest
// first.
func (ds *durableScope) shutdown(ctx context.Context, signal string) error {
	ds.mu.Lock()
	order := ds.rs.order
	ds.mu.Unlock()
	var errs []error
	for i := len(order) - 1; i >= 0; i-- {
		if h, ok := order[i].Interface().(OnApplicationShutdown); ok {
			if err := h.OnApplicationShutdown(ctx, signal); err != nil {
				errs = append(errs, fmt.Errorf("context %q: %s OnApplicationShutdown: %w", ds.rs.cid.ID, order[i].Type(), err))
			}
		}
	}
	return errors.Join(errs...)
}

// ReleaseContext drops the durable instances of a context ID, for example
// when a tenant is deleted, after calling OnApplicationShutdown on them. The
// next request with the ID builds new ones. Requests still using the old
// instances keep them.
func (a *App) ReleaseContext(ctx context.Context, id string) error {
	c := a.c
	c.durables.mu.Lock()
	ds := c.durables.scopes[id]
	delete(c.durables.scopes, id)
	c.durables.mu.Unlock()
	if ds == nil {
		return nil
	}
	return ds.shutdown(ctx, "")
}

// DurableContexts lists the context IDs that have durable instances.
func (a *App) DurableContexts() []string {
	c := a.c
	c.durables.mu.Lock()
	defer c.durables.mu.Unlock()
	out := make([]string, 0, len(c.durables.scopes))
	for id := range c.durables.scopes {
		out = append(out, id)
	}
	return out
}

// releaseAll shuts down every durable scope, at application shutdown.
func (c *container) releaseAll(ctx context.Context, signal string) error {
	c.durables.mu.Lock()
	scopes := c.durables.scopes
	c.durables.scopes = nil
	c.durables.mu.Unlock()
	var errs []error
	for _, ds := range scopes {
		errs = append(errs, ds.shutdown(ctx, signal))
	}
	return errors.Join(errs...)
}
