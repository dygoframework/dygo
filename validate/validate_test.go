package validate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/validate"
)

type body struct{ Name string }

func (b *body) Validate(context.Context) error {
	var v validate.Errors
	if b.Name == "" {
		v.Add("body", "name", "is required")
	}
	return v.Err()
}

type query struct{ Limit int }

func (q query) Validate(context.Context) error {
	var v validate.Errors
	if q.Limit > 100 {
		v.Add("query", "limit", "must be at most 100")
	}
	return v.Err()
}

type createIn struct {
	Body  body
	Query query
	Ptr   *body // nil pointers are skipped
}

func (in *createIn) Validate(context.Context) error {
	if in.Query.Limit < 0 {
		return validate.Errors{{Source: "query", Name: "limit", Reason: "must not be negative"}}.Err()
	}
	return nil
}

func TestMethodsMergesFieldErrors(t *testing.T) {
	ctx := context.Background()
	s := validate.Methods()
	if err := s.Validate(ctx, &createIn{Body: body{Name: "a"}}); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	err := s.Validate(ctx, &createIn{Query: query{Limit: 500}})
	var ve *validate.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 2 {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(ve.Error(), `body "name" is required`) || !strings.Contains(ve.Error(), `query "limit"`) {
		t.Fatalf("message %q", ve.Error())
	}
	err = s.Validate(ctx, &createIn{Query: query{Limit: -1}})
	if !errors.As(err, &ve) || len(ve.Fields) != 2 || ve.Fields[0].Reason != "must not be negative" {
		t.Fatalf("outer Validate should run first: %v", err)
	}
	// Values that are not struct pointers are fine.
	if err := s.Validate(ctx, 3); err != nil {
		t.Fatal(err)
	}
}

type embedded struct {
	query // promoted Validate: runs once, as the outer value's
}

func TestMethodsEmbeddedRunsOnce(t *testing.T) {
	err := validate.Methods().Validate(context.Background(), &embedded{query{Limit: 101}})
	var ve *validate.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 1 {
		t.Fatalf("got %v", err)
	}
}

type failing struct{ A, B query }

func TestOtherErrorsPassThrough(t *testing.T) {
	boom := errors.New("db down")
	s := validate.SchemaFunc(func(context.Context, any) error { return boom })
	if err := s.Validate(context.Background(), nil); err != boom {
		t.Fatalf("got %v", err)
	}
	// Two failing fields of the same type merge into one error.
	err := validate.Methods().Validate(context.Background(), &failing{A: query{Limit: 200}, B: query{Limit: 300}})
	var ve *validate.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 2 {
		t.Fatalf("got %v", err)
	}
}

func TestValidationErrorIs400(t *testing.T) {
	err := validate.Errors{{Source: "body", Name: "email", Reason: "must be a valid email"}}.Err()
	b := errs.ToBody(err)
	if b.StatusCode != 400 || b.Message != "validation failed" {
		t.Fatalf("%+v", b)
	}
	if fs, ok := b.Details.([]validate.FieldError); !ok || fs[0].Name != "email" {
		t.Fatalf("details %#v", b.Details)
	}
	if (validate.Errors{}).Err() != nil {
		t.Fatal("empty Errors should be nil")
	}
}
