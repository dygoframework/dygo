// Package validate checks handler input after binding. A [Schema] does the
// checking; [Pipe] and [Use] plug it into the pipeline:
//
//	app, err := dygo.Create(ctx, AppModule, validate.Use(validate.Methods()))
//
// [Methods] needs no dependency: it calls Validate(ctx) on input types that
// have one. The sub-module validate/playground adapts go-playground/validator
// for `validate:"required,email"` struct tags.
//
// A failed check is a *ValidationError, written as a 400 that lists every
// failing field in the same shape as binding errors:
//
//	{"statusCode":400,"error":"Bad Request","message":"validation failed",
//	 "details":[{"source":"body","name":"email","reason":"must be a valid email"}]}
package validate

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
)

// FieldError is one failing field: source ("body", "query", "path", ...),
// name, the value when it is safe to echo, and the reason.
type FieldError = dygo.FieldError

// Schema validates a value, usually a pointer to a handler's input struct. It
// returns nil, a *ValidationError, or another error (which is passed on as is).
type Schema interface {
	Validate(ctx context.Context, v any) error
}

// SchemaFunc is a function used as a [Schema].
type SchemaFunc func(ctx context.Context, v any) error

// Validate calls f.
func (f SchemaFunc) Validate(ctx context.Context, v any) error { return f(ctx, v) }

// ValidationError lists every failing field. It maps to a 400.
type ValidationError struct {
	Fields []FieldError
	shape  func([]FieldError) any
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = fmt.Sprintf("%s %q %s", f.Source, f.Name, f.Reason)
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// HTTPError returns the 400 response, implementing errs.Converter.
func (e *ValidationError) HTTPError() *errs.HTTPError {
	var details any = e.Fields
	if e.shape != nil {
		details = e.shape(e.Fields)
	}
	return errs.BadRequest("validation failed", errs.Details(details))
}

// Errors collects field errors inside a Validate method:
//
//	func (in *CreateIn) Validate(ctx context.Context) error {
//		var v validate.Errors
//		if in.Body.Name == "" {
//			v.Add("body", "name", "is required")
//		}
//		return v.Err()
//	}
type Errors []FieldError

// Add records a failing field.
func (e *Errors) Add(source, name, reason string) {
	*e = append(*e, FieldError{Source: source, Name: name, Reason: reason})
}

// Err returns a *ValidationError, or nil when nothing was added.
func (e Errors) Err() error {
	if len(e) == 0 {
		return nil
	}
	return &ValidationError{Fields: e}
}

// Validatable is implemented by input types that check themselves.
type Validatable interface {
	Validate(ctx context.Context) error
}

// Methods returns a Schema that calls Validate(ctx) on the value and on each of
// its direct fields (such as the Body field) that implement [Validatable]. An
// embedded struct's Validate is promoted, so it runs once, as the value's.
// Errors from several fields are merged into one *ValidationError.
func Methods() Schema { return SchemaFunc(validateMethods) }

var validatableType = reflect.TypeFor[Validatable]()

var methodFields sync.Map // reflect.Type -> []int: fields implementing Validatable through a pointer

func validateMethods(ctx context.Context, v any) error {
	var m merger
	if x, ok := v.(Validatable); ok {
		m.add(x.Validate(ctx))
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer && !rv.IsNil() && rv.Elem().Kind() == reflect.Struct {
		s := rv.Elem()
		for _, i := range fieldsOf(s.Type()) {
			f := s.Field(i)
			if f.Kind() == reflect.Pointer && f.IsNil() {
				continue
			}
			if f.Kind() != reflect.Pointer {
				f = f.Addr()
			}
			m.add(f.Interface().(Validatable).Validate(ctx))
		}
	}
	return m.err()
}

func fieldsOf(t reflect.Type) []int {
	if v, ok := methodFields.Load(t); ok {
		return v.([]int)
	}
	var idx []int
	// An embedded field's Validate is promoted to the struct, which is already called.
	outer := reflect.PointerTo(t).Implements(validatableType)
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() || (sf.Anonymous && outer) {
			continue
		}
		ft := sf.Type
		if ft.Kind() != reflect.Pointer {
			ft = reflect.PointerTo(ft)
		}
		if ft.Implements(validatableType) {
			idx = append(idx, i)
		}
	}
	methodFields.Store(t, idx)
	return idx
}

// merger joins validation errors without allocating when all pass. The first
// error that is not a *ValidationError wins as is.
type merger struct {
	first *ValidationError
	out   *ValidationError
	other error
}

func (m *merger) add(err error) {
	if err == nil || m.other != nil {
		return
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		m.other = err
		return
	}
	switch {
	case m.first == nil:
		m.first = ve
	case m.out == nil:
		m.out = &ValidationError{Fields: append(append([]FieldError(nil), m.first.Fields...), ve.Fields...)}
	default:
		m.out.Fields = append(m.out.Fields, ve.Fields...)
	}
}

func (m *merger) err() error {
	switch {
	case m.other != nil:
		return m.other
	case m.out != nil:
		return m.out
	case m.first != nil:
		return m.first
	}
	return nil
}

// Option configures [Pipe] and [Use].
type Option func(*config)

type config struct {
	shape func([]FieldError) any
}

// WithErrorShape changes what is sent as "details" for a validation error,
// for example a list of messages:
//
//	validate.WithErrorShape(func(fs []validate.FieldError) any {
//		msgs := make([]string, len(fs))
//		for i, f := range fs {
//			msgs[i] = f.Name + " " + f.Reason
//		}
//		return msgs
//	})
func WithErrorShape(fn func([]FieldError) any) Option {
	return func(c *config) { c.shape = fn }
}

// Pipe returns a pipe that validates handler input with s.
func Pipe(s Schema, opts ...Option) dygo.Pipe {
	var c config
	for _, o := range opts {
		o(&c)
	}
	return dygo.PipeFunc(func(ec dygo.ExecutionContext, in any) error {
		err := s.Validate(ec.Context(), in)
		if err == nil {
			return nil
		}
		var ve *ValidationError
		if c.shape != nil && errors.As(err, &ve) {
			cp := *ve
			cp.shape = c.shape
			return &cp
		}
		return err
	})
}

// Use validates the input of every typed route with s.
func Use(s Schema, opts ...Option) dygo.Option {
	return dygo.WithGlobalPipes(Pipe(s, opts...))
}

// Responses returns an interceptor that validates handler results with s. A
// result that fails is a bug on the server, so it becomes a 500 and the
// details are logged, not sent.
func Responses(s Schema) dygo.Interceptor {
	return dygo.InterceptorFunc(func(ec dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
		out, err := next.Handle()
		if err != nil || out == nil {
			return out, err
		}
		if verr := s.Validate(ec.Context(), out); verr != nil {
			return nil, errs.Internal("", errs.Cause(fmt.Errorf("invalid response: %w", verr)))
		}
		return out, nil
	})
}
