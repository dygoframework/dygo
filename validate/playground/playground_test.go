package playground_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dygoframework/dygo/validate"
	"github.com/dygoframework/dygo/validate/playground"
)

type item struct {
	Name string `json:"name" validate:"required"`
}

type Page struct {
	Limit int `query:"limit" validate:"lte=100"`
}

type createIn struct {
	Page
	ID     string `path:"id" validate:"len=4"`
	Tenant string `header:"X-Tenant" validate:"required"`
	Body   struct {
		Email string `json:"email" validate:"required,email"`
		Nick  string `validate:"min=3"`
		Items []item `json:"items" validate:"dive"`
	} `body:"json"`
}

func TestValidate(t *testing.T) {
	in := &createIn{ID: "abc"}
	in.Limit = 500
	in.Body.Email = "nope"
	in.Body.Nick = "ab"
	in.Body.Items = []item{{Name: "ok"}, {}}
	err := playground.New().Validate(context.Background(), in)
	var ve *validate.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("got %v", err)
	}
	want := []validate.FieldError{
		{Source: "query", Name: "limit", Value: "500", Reason: "must be at most 100"},
		{Source: "path", Name: "id", Value: "abc", Reason: "must be exactly 4 characters"},
		{Source: "header", Name: "X-Tenant", Value: "", Reason: "is required"},
		{Source: "body", Name: "email", Reason: "must be a valid email"},
		{Source: "body", Name: "Nick", Reason: "must be at least 3 characters"},
		{Source: "body", Name: "items[1].name", Reason: "is required"},
	}
	if !reflect.DeepEqual(ve.Fields, want) {
		t.Fatalf("fields\n got %+v\nwant %+v", ve.Fields, want)
	}
}

func TestValid(t *testing.T) {
	in := &createIn{ID: "abcd", Tenant: "t"}
	in.Body.Email = "a@b.co"
	in.Body.Nick = "abc"
	if err := playground.New().Validate(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if err := playground.New().Validate(context.Background(), "not a struct"); err != nil {
		t.Fatal(err)
	}
}
