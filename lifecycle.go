package dygo

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
)

// Lifecycle hooks. A provider or controller opts in by implementing the
// interface; the container finds it with a type assertion. Hooks are not called
// on transient instances.
//
// Order at startup: every OnModuleInit, module level by module level (modules
// that import nothing first), then every OnApplicationBootstrap in the same
// order. Order at shutdown is the reverse: OnModuleDestroy, then
// BeforeApplicationShutdown, then OnApplicationShutdown.
type (
	// OnModuleInit runs once the instance and all its dependencies are built.
	OnModuleInit interface {
		OnModuleInit(ctx context.Context) error
	}
	// OnApplicationBootstrap runs after every module has been initialized.
	OnApplicationBootstrap interface {
		OnApplicationBootstrap(ctx context.Context) error
	}
	// OnModuleDestroy runs first during shutdown.
	OnModuleDestroy interface {
		OnModuleDestroy(ctx context.Context) error
	}
	// BeforeApplicationShutdown runs after every OnModuleDestroy. signal is
	// "SIGINT" or "SIGTERM" when shutdown came from a signal, and empty otherwise.
	BeforeApplicationShutdown interface {
		BeforeApplicationShutdown(ctx context.Context, signal string) error
	}
	// OnApplicationShutdown runs last.
	OnApplicationShutdown interface {
		OnApplicationShutdown(ctx context.Context, signal string) error
	}
)

// hookOrder returns built singletons ordered for startup hooks.
func (c *container) hookOrder() []*node {
	ns := make([]*node, 0, len(c.built))
	seen := map[any]bool{}
	for _, n := range c.built {
		// The same pointer can be registered in several modules with Value.
		if v := n.instance; v.Comparable() && !v.IsZero() {
			key := v.Interface()
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		ns = append(ns, n)
	}
	sort.SliceStable(ns, func(i, j int) bool {
		if ns[i].mod.level != ns[j].mod.level {
			return ns[i].mod.level < ns[j].mod.level
		}
		return ns[i].order < ns[j].order
	})
	return ns
}

// runInit calls OnModuleInit then OnApplicationBootstrap. It returns the nodes
// that were initialized, so that a failed start can still shut them down.
func runInit(ctx context.Context, ns []*node) ([]*node, error) {
	var inited []*node
	for _, n := range ns {
		if h, ok := n.instance.Interface().(OnModuleInit); ok {
			if err := safeHook(func() error { return h.OnModuleInit(ctx) }); err != nil {
				return inited, fmt.Errorf("module %q: %s OnModuleInit: %w", n.mod.name, n, err)
			}
		}
		inited = append(inited, n)
	}
	for _, n := range ns {
		if h, ok := n.instance.Interface().(OnApplicationBootstrap); ok {
			if err := safeHook(func() error { return h.OnApplicationBootstrap(ctx) }); err != nil {
				return inited, fmt.Errorf("module %q: %s OnApplicationBootstrap: %w", n.mod.name, n, err)
			}
		}
	}
	return inited, nil
}

// safeHook runs a lifecycle hook and turns a panic into a *PanicError, so a
// panicking hook fails the boot (or is reported by shutdown) like an error,
// and the other shutdown hooks still run.
func safeHook(fn func() error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = &PanicError{Value: v, Stack: debug.Stack()}
		}
	}()
	return fn()
}

// runShutdown calls the shutdown hooks in reverse order. Every hook runs even
// when an earlier one fails; all errors are returned joined. drain, when not
// nil, runs between BeforeApplicationShutdown and OnApplicationShutdown; the
// HTTP server uses it to stop accepting and finish in-flight requests.
func runShutdown(ctx context.Context, ns []*node, signal string, drain func(context.Context) error) error {
	var errs []error
	call := func(name string, fn func(n *node) error) {
		for i := len(ns) - 1; i >= 0; i-- {
			if err := safeHook(func() error { return fn(ns[i]) }); err != nil {
				errs = append(errs, fmt.Errorf("module %q: %s %s: %w", ns[i].mod.name, ns[i], name, err))
			}
		}
	}
	call("OnModuleDestroy", func(n *node) error {
		if h, ok := n.instance.Interface().(OnModuleDestroy); ok {
			return h.OnModuleDestroy(ctx)
		}
		return nil
	})
	call("BeforeApplicationShutdown", func(n *node) error {
		if h, ok := n.instance.Interface().(BeforeApplicationShutdown); ok {
			return h.BeforeApplicationShutdown(ctx, signal)
		}
		return nil
	})
	if drain != nil {
		if err := drain(ctx); err != nil {
			errs = append(errs, fmt.Errorf("draining connections: %w", err))
		}
	}
	call("OnApplicationShutdown", func(n *node) error {
		if h, ok := n.instance.Interface().(OnApplicationShutdown); ok {
			return h.OnApplicationShutdown(ctx, signal)
		}
		return nil
	})
	return errors.Join(errs...)
}
