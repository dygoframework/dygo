// Package playground adapts github.com/go-playground/validator/v10 to
// validate.Schema, so input structs can use `validate:"..."` tags:
//
//	type CreateIn struct {
//		Body struct {
//			Email string `json:"email" validate:"required,email"`
//			Age   int    `json:"age"   validate:"gte=18"`
//		} `body:"json"`
//		Limit int `query:"limit" validate:"lte=100"`
//	}
//
//	app, err := dygo.Create(ctx, AppModule, validate.Use(playground.New()))
//
// Failing fields are reported with their request source and name (the json,
// query, path, header, cookie or form name), not the Go field name.
//
// It is a separate Go module so that the core stays free of dependencies.
package playground

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/dygoframework/dygo/validate"
	"github.com/go-playground/validator/v10"
)

// Schema validates with a *validator.Validate.
type Schema struct {
	v *validator.Validate
}

// New returns a Schema using v, or a new validator with required struct
// checking enabled when v is nil. Register custom validations on v first.
func New(v ...*validator.Validate) *Schema {
	if len(v) > 0 && v[0] != nil {
		return &Schema{v: v[0]}
	}
	return &Schema{v: validator.New(validator.WithRequiredStructEnabled())}
}

// Validator returns the underlying validator, to register custom rules.
func (s *Schema) Validator() *validator.Validate { return s.v }

// Validate implements validate.Schema. Values that are not structs or
// pointers to structs pass.
func (s *Schema) Validate(ctx context.Context, v any) error {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	err := s.v.StructCtx(ctx, v)
	if err == nil {
		return nil
	}
	var ves validator.ValidationErrors
	if !errors.As(err, &ves) {
		return err
	}
	fields := make([]validate.FieldError, 0, len(ves))
	for _, fe := range ves {
		src, name := locate(t, fe.StructNamespace())
		f := validate.FieldError{Source: src, Name: name, Reason: reason(fe)}
		if src != "body" && src != "" {
			f.Value = fmt.Sprint(fe.Value())
		}
		fields = append(fields, f)
	}
	return &validate.ValidationError{Fields: fields}
}

var sources = []string{"path", "query", "header", "cookie", "form", "file"}

// locate turns a namespace like "CreateIn.Body.Items[0].Name" into the request
// source and the client facing name, "body" and "items[0].name".
func locate(t reflect.Type, ns string) (source, name string) {
	parts := strings.Split(ns, ".")[1:] // drop the type name
	var names []string
	for _, p := range parts {
		field, index, _ := strings.Cut(p, "[")
		if index != "" {
			index = "[" + index
		}
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			names = append(names, p)
			continue
		}
		sf, ok := t.FieldByName(field)
		if !ok {
			names = append(names, p)
			continue
		}
		t = sf.Type
		if sf.Anonymous {
			continue
		}
		if source == "" {
			if _, ok := sf.Tag.Lookup("body"); ok {
				source = "body"
				if index != "" {
					names = append(names, index)
				}
				continue
			}
			for _, s := range sources {
				if v, ok := sf.Tag.Lookup(s); ok {
					source = s
					names = append(names, v+index)
					break
				}
			}
			if source != "" {
				continue
			}
		}
		n := field
		if j, ok := sf.Tag.Lookup("json"); ok {
			if j, _, _ = strings.Cut(j, ","); j != "" && j != "-" {
				n = j
			}
		}
		names = append(names, n+index)
	}
	name = strings.Join(names, ".")
	name = strings.ReplaceAll(name, ".[", "[")
	if source == "" {
		source = "body"
	}
	return source, name
}

func reason(fe validator.FieldError) string {
	p := fe.Param()
	isString := fe.Kind() == reflect.String
	isCollection := fe.Kind() == reflect.Slice || fe.Kind() == reflect.Map || fe.Kind() == reflect.Array
	unit := ""
	switch {
	case isString:
		unit = " characters"
	case isCollection:
		unit = " items"
	}
	switch fe.Tag() {
	case "required", "required_if", "required_unless", "required_with", "required_without":
		return "is required"
	case "min", "gte":
		return "must be at least " + p + unit
	case "max", "lte":
		return "must be at most " + p + unit
	case "gt":
		return "must be more than " + p + unit
	case "lt":
		return "must be less than " + p + unit
	case "len":
		return "must be exactly " + p + unit
	case "eq":
		return "must be " + p
	case "ne":
		return "must not be " + p
	case "oneof":
		return "must be one of: " + p
	case "email":
		return "must be a valid email"
	case "url", "http_url":
		return "must be a valid URL"
	case "uuid", "uuid4":
		return "must be a valid UUID"
	case "alphanum":
		return "must contain only letters and digits"
	case "numeric", "number":
		return "must be numeric"
	}
	if p != "" {
		return fmt.Sprintf("failed %s=%s", fe.Tag(), p)
	}
	return "failed " + fe.Tag()
}
