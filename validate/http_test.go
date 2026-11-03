package validate_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/validate"
)

// These tests run the pipe and the interceptor inside a real application.

type itemIn struct {
	Body struct {
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	} `body:"json"`
}

func (in *itemIn) Validate(context.Context) error {
	var v validate.Errors
	if in.Body.Name == "" {
		v.Add("body", "name", "is required")
	}
	if in.Body.Limit < 0 {
		return errors.New("limit is broken")
	}
	return v.Err()
}

type itemOut struct {
	Name string `json:"name"`
}

func (o itemOut) Validate(context.Context) error {
	var v validate.Errors
	if o.Name == "bad" {
		v.Add("body", "name", "is not allowed in a response")
	}
	return v.Err()
}

type itemController struct{}

func (itemController) Routes(r dygo.Router) {
	r.Post("/items", dygo.Handle(func(_ context.Context, in itemIn) (itemOut, error) {
		return itemOut{Name: in.Body.Name}, nil
	}))
	r.Get("/nothing", dygo.Handle(func(context.Context, struct{}) (any, error) {
		return nil, nil
	}))
}

var itemModule = &dygo.Module{
	Name:        "items",
	Controllers: []dygo.Provider{dygo.Value(itemController{})},
}

func TestPipeAndResponses(t *testing.T) {
	shape := validate.WithErrorShape(func(fs []validate.FieldError) any {
		msgs := make([]string, len(fs))
		for i, f := range fs {
			msgs[i] = f.Name + " " + f.Reason
		}
		return msgs
	})
	tests := []struct {
		name   string
		opts   []validate.Option
		body   map[string]any
		status int
		want   string
	}{
		{"valid", nil, map[string]any{"name": "pen"}, http.StatusCreated, `"name":"pen"`},
		{"invalid", nil, map[string]any{"name": ""}, http.StatusBadRequest, `"reason":"is required"`},
		{"invalid with shape", []validate.Option{shape}, map[string]any{"name": ""}, http.StatusBadRequest, `"name is required"`},
		{"other error", []validate.Option{shape}, map[string]any{"name": "x", "limit": -1}, http.StatusInternalServerError, ""},
		{"bad response", nil, map[string]any{"name": "bad"}, http.StatusInternalServerError, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := dygotest.New(itemModule).
				With(validate.Use(validate.Methods(), tt.opts...)).
				With(dygo.WithGlobalInterceptors(validate.Responses(validate.Methods()))).
				CompileHTTP(t)
			res := srv.POST("/items").JSON(tt.body).Do(t).Status(t, tt.status)
			if !strings.Contains(res.Text(), tt.want) {
				t.Fatalf("body %s does not contain %s", res.Text(), tt.want)
			}
		})
	}
}

func TestResponsesSkipsNil(t *testing.T) {
	srv := dygotest.New(itemModule).
		With(dygo.WithGlobalInterceptors(validate.Responses(validate.Methods()))).
		CompileHTTP(t)
	srv.GET("/nothing").Do(t).Status(t, http.StatusOK)
}

func TestMergeThreeAndOther(t *testing.T) {
	in := &three{A: failer{name: "a"}, B: failer{name: "b"}, C: failer{name: "c"}}
	err := validate.Methods().Validate(context.Background(), in)
	var ve *validate.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 3 {
		t.Fatalf("err = %v", err)
	}
	boom := errors.New("boom")
	in = &three{A: failer{name: "a"}, B: failer{err: boom}, C: failer{name: "c"}}
	if err := validate.Methods().Validate(context.Background(), in); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

// failer fails with a field error for name, or with err when set.
type failer struct {
	name string
	err  error
}

func (f failer) Validate(context.Context) error {
	if f.err != nil {
		return f.err
	}
	var v validate.Errors
	v.Add("body", f.name, "is wrong")
	return v.Err()
}

type three struct{ A, B, C failer }
