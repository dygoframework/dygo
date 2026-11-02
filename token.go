package dygo

import (
	"fmt"
	"reflect"
)

// Token identifies a provider in the container. Create one with [TypeOf] or
// [NewToken].
type Token interface {
	// String describes the token in error messages.
	String() string
	// Type is the Go type of the value the token resolves to.
	Type() reflect.Type

	key() tokenKey
}

// tokenKey is the comparable identity of a token. A type token has an empty name.
type tokenKey struct {
	typ  reflect.Type
	name string
}

func (k tokenKey) String() string {
	if k.name == "" {
		return typeName(k.typ)
	}
	return fmt.Sprintf("%q (%s)", k.name, typeName(k.typ))
}

type typeToken struct{ t reflect.Type }

func (t typeToken) String() string     { return typeName(t.t) }
func (t typeToken) Type() reflect.Type { return t.t }
func (t typeToken) key() tokenKey      { return tokenKey{typ: t.t} }

// TypeOf returns the token for type T. This is the token a constructor
// returning T is registered under, and the token a constructor parameter of
// type T asks for.
func TypeOf[T any]() Token { return typeToken{reflect.TypeFor[T]()} }

// NamedToken is a typed, named token. Two values of the same type can be told
// apart by name:
//
//	var PrimaryDB = dygo.NewToken[*sql.DB]("db.primary")
//	dygo.Provide(OpenPrimary, dygo.For(PrimaryDB))
//
// Inject it with a field of type *sql.DB tagged name:"db.primary" in a
// parameter struct that embeds [In].
type NamedToken[T any] struct{ name string }

// NewToken creates a named token for values of type T. It panics if name is
// empty, because an empty name would collide with the plain type token.
func NewToken[T any](name string) NamedToken[T] {
	if name == "" {
		panic("dygo.NewToken: name must not be empty")
	}
	return NamedToken[T]{name: name}
}

// Name returns the token's name.
func (t NamedToken[T]) Name() string { return t.name }

// String describes the token in error messages.
func (t NamedToken[T]) String() string { return t.key().String() }

// Type returns T.
func (t NamedToken[T]) Type() reflect.Type { return reflect.TypeFor[T]() }

func (t NamedToken[T]) key() tokenKey { return tokenKey{typ: reflect.TypeFor[T](), name: t.name} }

func typeName(t reflect.Type) string {
	if t == nil {
		return "<nil>"
	}
	return t.String()
}
