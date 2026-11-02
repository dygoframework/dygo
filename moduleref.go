package dygo

import (
	"errors"
	"fmt"
	"reflect"
)

// ModuleRef gives a provider access to the container from its own module's
// point of view. Inject it as a *dygo.ModuleRef
// constructor parameter.
type ModuleRef struct {
	c  *container
	mi *moduleInfo
}

// Module returns the name of the module this reference belongs to.
func (r *ModuleRef) Module() string { return r.mi.name }

// Get returns the instance for tok if it is visible in this module: its own
// providers, its imports' exports, or a global module's exports. A transient
// provider is built anew on each call.
func (r *ModuleRef) Get(tok Token) (any, error) {
	return r.c.get(r.mi, tok.key())
}

// App returns the application, for packages that discover handlers and
// build their pipelines with [NewEnhancers] from a provider (package ws).
// It is nil while constructors run; call it from OnModuleInit or later.
func (r *ModuleRef) App() *App { return r.c.app }

// Create calls ctor with its parameters resolved from this module, without
// registering the result. The returned value
// gets no lifecycle hooks.
func (r *ModuleRef) Create(ctor any) (any, error) {
	p := Provide(ctor, WithScope(Transient))
	if p.err != nil {
		return nil, p.err
	}
	n, errs := r.c.newNode(r.mi, p, false)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if errs := r.c.link(n); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	v, err := r.c.construct(nil, n)
	if err != nil {
		return nil, err
	}
	return v.Interface(), nil
}

// Construct calls ctor through ref and returns its result as T.
func Construct[T any](ref *ModuleRef, ctor any) (T, error) {
	var zero T
	v, err := ref.Create(ctor)
	if err != nil {
		return zero, err
	}
	t, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("dygo: constructor returned %s, not %s", reflect.TypeOf(v), reflect.TypeFor[T]())
	}
	return t, nil
}
