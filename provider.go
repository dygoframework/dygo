package dygo

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
)

// Scope controls how many instances of a provider exist.
type Scope int

const (
	// Singleton providers are built once at boot and shared. This is the default.
	Singleton Scope = iota
	// Transient providers are built again for every consumer that injects them.
	// Lifecycle hooks are not called on transient instances.
	Transient
	// RequestScoped providers are built at most once per HTTP request. Only
	// other request scoped providers may inject them directly; singletons and
	// controllers use [Scoped] instead. Lifecycle hooks are not called on them;
	// implement [OnRequestEnd] for per-request cleanup.
	RequestScoped
)

func (s Scope) String() string {
	switch s {
	case Singleton:
		return "singleton"
	case Transient:
		return "transient"
	case RequestScoped:
		return "request"
	default:
		return fmt.Sprintf("Scope(%d)", int(s))
	}
}

type providerKind int

const (
	kindConstructor providerKind = iota
	kindValue
	kindAlias
)

// Provider describes how the container gets a value. Create one with
// [Provide], [Value] or [Alias]. The zero Provider is invalid.
type Provider struct {
	kind   providerKind
	fn     reflect.Value
	value  reflect.Value
	target Token
	tokens []Token
	as     []reflect.Type
	groups []string
	scope  Scope
	// durable request scoped providers are kept per context ID (see Durable).
	durable bool
	src     string
	err     error
}

// ProvideOption configures a [Provider].
type ProvideOption func(*Provider)

var errorType = reflect.TypeFor[error]()

// Provide registers a constructor function. The container calls it once (or
// once per consumer for [Transient]) with its parameters resolved by type.
//
// Accepted shapes:
//
//	func(deps...) T
//	func(deps...) (T, error)
//
// A parameter may be any provided type, context.Context (the boot context),
// *ModuleRef, [Optional], [Lazy], or a struct embedding [In].
//
// By default the provider is registered under TypeOf[T]. [For] replaces that
// token, [As] adds more, and [Group] alone contributes only to the group.
func Provide(ctor any, opts ...ProvideOption) Provider {
	p := Provider{kind: kindConstructor}
	fn := reflect.ValueOf(ctor)
	switch {
	case ctor == nil:
		p.err = errors.New("dygo.Provide: constructor is nil")
	case fn.Kind() != reflect.Func:
		p.err = fmt.Errorf("dygo.Provide: expected a constructor function, got %s", fn.Type())
	default:
		p.fn = fn
		p.src = funcName(fn)
		ft := fn.Type()
		switch {
		case ft.IsVariadic():
			p.err = fmt.Errorf("constructor %s must not be variadic", p.src)
		case ft.NumOut() == 1 && ft.Out(0) == errorType, ft.NumOut() == 0 || ft.NumOut() > 2:
			p.err = fmt.Errorf("constructor %s must return T or (T, error)", p.src)
		case ft.NumOut() == 2 && ft.Out(1) != errorType:
			p.err = fmt.Errorf("constructor %s: second return value must be error, got %s", p.src, ft.Out(1))
		}
	}
	for _, o := range opts {
		o(&p)
	}
	return p
}

// Value registers an existing value. The default token is
// the value's dynamic type; use [For] or [As] to register it under an interface
// or named token.
func Value(v any, opts ...ProvideOption) Provider {
	p := Provider{kind: kindValue}
	if v == nil {
		p.err = errors.New("dygo.Value: value is nil; use For with a typed nil or a constructor")
	} else {
		p.value = reflect.ValueOf(v)
		p.src = "value " + typeName(p.value.Type())
	}
	for _, o := range opts {
		o(&p)
	}
	return p
}

// Alias makes alias resolve to the same instance as target. The target's type
// must be assignable to the alias's type.
func Alias(alias, target Token) Provider {
	p := Provider{kind: kindAlias, target: target, tokens: []Token{alias}}
	if alias == nil || target == nil {
		p.err = errors.New("dygo.Alias: tokens must not be nil")
	} else {
		p.src = "alias of " + target.String()
	}
	return p
}

// As also registers the provider under type T, which is usually an interface
// the concrete type implements. The same instance is shared across all tokens.
func As[T any]() ProvideOption {
	return func(p *Provider) { p.as = append(p.as, reflect.TypeFor[T]()) }
}

// For registers the provider under tok instead of its own type. Use it with
// [NewToken] for named values. Can be given more than once.
func For(tok Token) ProvideOption {
	return func(p *Provider) { p.tokens = append(p.tokens, tok) }
}

// Group adds the provider to a named group. Consumers receive every member as a
// slice through a field tagged group:"name" in an [In] struct. Groups are
// application wide. A provider with a group and no [For] or [As] option is not
// registered under its own type.
func Group(name string) ProvideOption {
	return func(p *Provider) { p.groups = append(p.groups, name) }
}

// WithScope sets the provider's scope. The default is [Singleton].
func WithScope(s Scope) ProvideOption {
	return func(p *Provider) { p.scope = s }
}

// Durable makes a [RequestScoped] provider durable: it is built once per
// context ID (usually a tenant) chosen by [WithContextIDStrategy] and reused
// by every request with that ID, instead of once per request. A durable
// provider may inject [ContextID] for the ID and payload, singletons and
// other durable providers, but not *http.Request or a request scoped provider
// that is not durable. Its instances get OnApplicationShutdown at shutdown or
// on [App.ReleaseContext]; OnRequestEnd is not called.
func Durable() ProvideOption {
	return func(p *Provider) { p.durable = true }
}

// outType is the type of the value the provider produces.
func (p *Provider) outType() reflect.Type {
	switch p.kind {
	case kindConstructor:
		if p.fn.IsValid() {
			return p.fn.Type().Out(0)
		}
	case kindValue:
		if p.value.IsValid() {
			return p.value.Type()
		}
	case kindAlias:
		if p.tokens[0] != nil {
			return p.tokens[0].Type()
		}
	}
	return nil
}

// registeredKeys lists the tokens this provider answers to.
func (p *Provider) registeredKeys() []tokenKey {
	var keys []tokenKey
	for _, t := range p.tokens {
		if t != nil {
			keys = append(keys, t.key())
		}
	}
	if len(p.tokens) == 0 && len(p.groups) == 0 {
		keys = append(keys, tokenKey{typ: p.outType()})
	}
	for _, t := range p.as {
		keys = append(keys, tokenKey{typ: t})
	}
	return keys
}

func funcName(fn reflect.Value) string {
	f := runtime.FuncForPC(fn.Pointer())
	if f == nil {
		return typeName(fn.Type())
	}
	name := f.Name()
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return name
}
