package dygo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
)

// In marks a parameter struct. Embed it in a struct used as a constructor
// parameter and the container fills each exported field:
//
//	type Params struct {
//		dygo.In
//		DB      *sql.DB `name:"db.primary"`
//		Cache   Cache   `optional:"true"`
//		Plugins []Plugin `group:"plugins"`
//	}
//
// Supported tags: name (named token, see [NewToken]), optional ("true" gives the
// zero value when nothing is provided) and group (a slice of group members).
type In struct{}

// Optional is a dependency that may be missing.
type Optional[T any] struct {
	value T
	ok    bool
}

// Get returns the value and whether it was provided.
func (o Optional[T]) Get() (T, bool) { return o.value, o.ok }

// OrElse returns the value, or def when it was not provided.
func (o Optional[T]) OrElse(def T) T {
	if o.ok {
		return o.value
	}
	return def
}

func (Optional[T]) dygoOptionalElem() reflect.Type { return reflect.TypeFor[T]() }

func (o *Optional[T]) dygoSetOptional(v any) {
	if v != nil {
		o.value = v.(T)
	}
	o.ok = true
}

// Lazy defers resolving a dependency until Get is called, which is how a
// dependency cycle is broken. Do not call
// Get inside a constructor; call it later, for example in OnModuleInit or in a
// request handler.
type Lazy[T any] struct{ r *lazyRef }

type lazyRef struct {
	once    sync.Once
	resolve func() (any, error)
	v       any
	err     error
}

// Get resolves the dependency. The result is cached after the first call.
func (l Lazy[T]) Get() (T, error) {
	var zero T
	if l.r == nil {
		return zero, errors.New("dygo.Lazy: not injected by the container")
	}
	l.r.once.Do(func() { l.r.v, l.r.err = l.r.resolve() })
	if l.r.err != nil {
		return zero, l.r.err
	}
	if l.r.v == nil {
		return zero, nil
	}
	return l.r.v.(T), nil
}

// MustGet is Get that panics on error.
func (l Lazy[T]) MustGet() T {
	v, err := l.Get()
	if err != nil {
		panic(err)
	}
	return v
}

func (Lazy[T]) dygoLazyElem() reflect.Type { return reflect.TypeFor[T]() }

func (l *Lazy[T]) dygoSetLazy(r *lazyRef) { l.r = r }

// Scoped gives a singleton (for example a controller) access to a
// [RequestScoped] provider. Get returns the instance for the request carried by
// ctx, building it on first use within that request.
//
//	type Controller struct{ tx dygo.Scoped[*sql.Tx] }
//
//	func (c *Controller) Create(ctx context.Context, in CreateIn) (*User, error) {
//		tx, err := c.tx.Get(ctx)
//		...
//	}
type Scoped[T any] struct{ r *scopedRef }

type scopedRef struct {
	c      *container
	target *node
}

// Get resolves the value for the request in ctx. It returns an error when ctx
// does not belong to a request handled by Dygo.
func (s Scoped[T]) Get(ctx context.Context) (T, error) {
	var zero T
	if s.r == nil {
		return zero, errors.New("dygo.Scoped: not injected by the container")
	}
	v, err := s.r.c.ensure(scopeFrom(ctx), s.r.target)
	if err != nil {
		return zero, err
	}
	if !v.IsValid() || (v.Kind() == reflect.Interface && v.IsNil()) {
		return zero, nil
	}
	return v.Interface().(T), nil
}

// MustGet is Get that panics on error.
func (s Scoped[T]) MustGet(ctx context.Context) T {
	v, err := s.Get(ctx)
	if err != nil {
		panic(err)
	}
	return v
}

func (Scoped[T]) dygoScopedElem() reflect.Type { return reflect.TypeFor[T]() }

func (s *Scoped[T]) dygoSetScoped(r *scopedRef) { s.r = r }

type (
	scopedMarker interface{ dygoScopedElem() reflect.Type }
	scopedSetter interface{ dygoSetScoped(*scopedRef) }
)

type (
	optionalMarker interface{ dygoOptionalElem() reflect.Type }
	optionalSetter interface{ dygoSetOptional(any) }
	lazyMarker     interface{ dygoLazyElem() reflect.Type }
	lazySetter     interface{ dygoSetLazy(*lazyRef) }
)

var (
	inType             = reflect.TypeFor[In]()
	contextType        = reflect.TypeFor[context.Context]()
	moduleRefType      = reflect.TypeFor[*ModuleRef]()
	contextIDType      = reflect.TypeFor[ContextID]()
	optionalMarkerType = reflect.TypeFor[optionalMarker]()
	lazyMarkerType     = reflect.TypeFor[lazyMarker]()
	scopedMarkerType   = reflect.TypeFor[scopedMarker]()
	requestType        = reflect.TypeFor[*http.Request]()
)

type depKind int

const (
	depPlain depKind = iota
	depOptional
	depLazy
	depGroup
	depContext
	depModuleRef
	depScoped
	depRequest
	depContextID
)

// dep is one value a constructor needs.
type dep struct {
	kind     depKind
	typ      reflect.Type // the parameter or field type
	key      tokenKey     // looked up token for plain, optional and lazy
	group    string
	optional bool // plain dependency tagged optional:"true"
	field    int  // field index in an In struct, -1 for a direct parameter
	label    string

	target  *node   // resolved provider, nil when optional and missing
	members []*node // resolved group members
}

// param is one constructor parameter, either a single dependency or an In
// struct holding several.
type param struct {
	typ  reflect.Type
	isIn bool
	deps []*dep
}

func isInStruct(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type == inType {
			return true
		}
	}
	return false
}

// analyzeParams reads a constructor's parameters.
func analyzeParams(ft reflect.Type) ([]*param, []error) {
	var params []*param
	var errs []error
	for i := 0; i < ft.NumIn(); i++ {
		t := ft.In(i)
		if !isInStruct(t) {
			d, err := classify(t, "", false, "", fmt.Sprintf("parameter %d", i+1))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			d.field = -1
			params = append(params, &param{typ: t, deps: []*dep{d}})
			continue
		}
		p := &param{typ: t, isIn: true}
		for j := 0; j < t.NumField(); j++ {
			f := t.Field(j)
			if f.Anonymous && f.Type == inType {
				continue
			}
			if !f.IsExported() {
				errs = append(errs, fmt.Errorf("field %s of %s must be exported to be injected", f.Name, t))
				continue
			}
			d, err := classify(f.Type, f.Tag.Get("name"), f.Tag.Get("optional") == "true", f.Tag.Get("group"),
				fmt.Sprintf("field %s.%s", t.Name(), f.Name))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			d.field = j
			p.deps = append(p.deps, d)
		}
		params = append(params, p)
	}
	return params, errs
}

func classify(t reflect.Type, name string, optional bool, group, label string) (*dep, error) {
	d := &dep{typ: t, label: label, optional: optional}
	switch {
	case group != "":
		if t.Kind() != reflect.Slice {
			return nil, fmt.Errorf("%s: group %q must be injected into a slice, got %s", label, group, t)
		}
		d.kind, d.group = depGroup, group
	case t == contextType:
		d.kind = depContext
	case t == moduleRefType:
		d.kind = depModuleRef
	case t.Implements(optionalMarkerType):
		elem := reflect.Zero(t).Interface().(optionalMarker).dygoOptionalElem()
		d.kind, d.key = depOptional, tokenKey{typ: elem, name: name}
	case t == requestType:
		d.kind = depRequest
	case t == contextIDType:
		d.kind = depContextID
	case t.Implements(scopedMarkerType):
		elem := reflect.Zero(t).Interface().(scopedMarker).dygoScopedElem()
		d.kind, d.key = depScoped, tokenKey{typ: elem, name: name}
	case t.Implements(lazyMarkerType):
		elem := reflect.Zero(t).Interface().(lazyMarker).dygoLazyElem()
		d.kind, d.key = depLazy, tokenKey{typ: elem, name: name}
	default:
		d.kind, d.key = depPlain, tokenKey{typ: t, name: name}
	}
	return d, nil
}

// lookupKey reports whether the dependency needs a provider lookup.
func (d *dep) lookupKey() bool {
	return d.kind == depPlain || d.kind == depOptional || d.kind == depLazy || d.kind == depScoped
}

func (d *dep) isOptional() bool { return d.kind == depOptional || d.optional }
