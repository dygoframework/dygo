package dygo

import (
	"net/http"
	"reflect"

	"github.com/dygoframework/dygo/internal/binder"
)

// BindError is returned (and written as a 400) when request input does not fit
// the handler's input struct. It lists every failing field.
type BindError = binder.Error

// FieldError is one failing field in a [BindError].
type FieldError = binder.FieldError

// RegisterConverter makes T usable in path, query, header, cookie and form
// fields. Types implementing encoding.TextUnmarshaler need no registration.
// Call it before creating the application, for example in an init function.
func RegisterConverter[T any](fn func(string) (T, error)) {
	binder.RegisterConverter(reflect.TypeFor[T](), func(s string) (any, error) { return fn(s) })
}

// RegisterSource adds a custom input tag. A field tagged name:"" receives
// fn's result:
//
//	dygo.RegisterSource("user", func(r *http.Request) (any, error) { return auth.UserFrom(r) })
//
//	type In struct {
//		User *auth.User `user:""`
//	}
//
// Call it before creating the application.
func RegisterSource(name string, fn func(*http.Request) (any, error)) {
	binder.RegisterSource(name, fn)
}
