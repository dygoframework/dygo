package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sync"

	"github.com/dygoframework/dygo/openapi"
	"github.com/dygoframework/dygo/validate"
)

// MaxFixes is the most times [Generate] may send a bad answer back to be
// fixed.
const MaxFixes = 3

// GenerateOption changes how [Generate] works.
type GenerateOption func(*generateOptions)

type generateOptions struct {
	fixes int
	check validate.Schema
}

// Fixes sets how many times a bad answer is sent back to the model to fix,
// from 0 to [MaxFixes]. The default is 1.
func Fixes(n int) GenerateOption {
	return func(o *generateOptions) { o.fixes = min(max(n, 0), MaxFixes) }
}

// CheckWith checks the decoded value with s instead of [validate.Methods].
// Pass playground.New() to check `validate:"..."` struct tags.
func CheckWith(s validate.Schema) GenerateOption {
	return func(o *generateOptions) { o.check = s }
}

// Generate asks m for an answer shaped like T and decodes it.
//
//	type Ticket struct {
//		Title    string `json:"title" validate:"required,max=80"`
//		Priority string `json:"priority" validate:"oneof=low medium high"`
//	}
//	t, err := ai.Generate[Ticket](ctx, m, ai.Prompt("Make a ticket from: "+email))
//
// T must be a struct. Its JSON Schema comes from the json and validate tags,
// the same way openapi builds one, and the provider asks the vendor for JSON
// of that shape. The answer is decoded, then checked with [validate.Methods]
// (or the schema given with [CheckWith]). When decoding or the check fails,
// the error goes back to the model once to fix (see [Fixes]); after that
// Generate fails with an error wrapping [ErrBadOutput].
func Generate[T any](ctx context.Context, m Model, req Request, opts ...GenerateOption) (T, error) {
	var zero T
	o := generateOptions{fixes: 1, check: validate.Methods()}
	for _, opt := range opts {
		opt(&o)
	}
	s, err := SchemaFor[T]()
	if err != nil {
		return zero, err
	}
	req.Schema = s
	req.Messages = slices.Clip(req.Messages) // appends below must not touch the caller's array
	for attempt := 0; ; attempt++ {
		res, err := m.Chat(ctx, req)
		if err != nil {
			return zero, err
		}
		out, reason, err := decode[T](ctx, res, o.check)
		if err != nil {
			return zero, err
		}
		if reason == "" {
			return out, nil
		}
		if attempt >= o.fixes {
			return zero, fmt.Errorf("%w: %s", ErrBadOutput, reason)
		}
		req.Messages = append(req.Messages, Assistant(res.Text()),
			User("Your answer was not valid: "+reason+". Answer again with only the corrected JSON."))
	}
}

// decode reads the answer into a T. A bad answer the model may fix gives a
// reason; an answer it cannot fix gives an error.
func decode[T any](ctx context.Context, res Response, check validate.Schema) (T, string, error) {
	var out T
	switch res.StopReason {
	case "refused":
		return out, "", ErrRefused
	case "max_tokens":
		return out, "", fmt.Errorf("%w: the answer was cut at MaxTokens", ErrBadOutput)
	}
	if err := json.Unmarshal(stripFence([]byte(res.Text())), &out); err != nil {
		return out, "it is not JSON of the right shape (" + err.Error() + ")", nil
	}
	if check == nil {
		return out, "", nil
	}
	target := any(&out)
	if reflect.TypeFor[T]().Kind() == reflect.Pointer {
		target = out // already a pointer, as Validate methods expect
	}
	if err := check.Validate(ctx, target); err != nil {
		if _, ok := errors.AsType[*validate.ValidationError](err); ok {
			return out, err.Error(), nil
		}
		return out, "", err
	}
	return out, "", nil
}

// stripFence removes a ```json ... ``` fence some models put around JSON.
func stripFence(b []byte) []byte {
	b = bytes.TrimSpace(b)
	if !bytes.HasPrefix(b, []byte("```")) {
		return b
	}
	b = b[3:]
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	b, _ = bytes.CutSuffix(bytes.TrimSpace(b), []byte("```"))
	return bytes.TrimSpace(b)
}

var schemas sync.Map // reflect.Type -> *Schema

// SchemaFor returns the JSON Schema [Generate] sends for T, which must be a
// struct. It is built once per type.
//
// Limits such as max=80 are left out of the schema, because vendors' JSON
// modes refuse most of them; [Generate] checks them after decoding instead.
// Maps and recursive types are not supported by every vendor.
func SchemaFor[T any]() (*Schema, error) {
	t := reflect.TypeFor[T]()
	if s, ok := schemas.Load(t); ok {
		return s.(*Schema), nil
	}
	st := t
	for st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() != reflect.Struct {
		return nil, invalid("Generate needs a struct type, not %s", t)
	}
	var tree any
	b, _ := json.Marshal(openapi.JSONSchema(t))
	_ = json.Unmarshal(b, &tree)
	b, _ = json.Marshal(strict(tree))
	s := &Schema{Name: schemaName(st), JSON: b}
	schemas.Store(t, s)
	return s, nil
}

var notName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func schemaName(t reflect.Type) string {
	n := notName.ReplaceAllString(t.Name(), "_")
	if n == "" || n == "_" {
		return "output"
	}
	return n[:min(len(n), 64)]
}

// dropped are the keywords vendors' JSON modes refuse.
var dropped = []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
	"minLength", "maxLength", "minItems", "maxItems", "default", "examples"}

// formats are the string formats vendors accept; others are left out.
var formats = map[string]bool{"date-time": true, "time": true, "date": true, "duration": true,
	"email": true, "hostname": true, "uri": true, "ipv4": true, "ipv6": true, "uuid": true}

// strict trims a schema to what vendors' JSON modes accept, and closes
// objects to extra fields.
func strict(v any) any {
	s, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for _, k := range dropped {
		delete(s, k)
	}
	if f, ok := s["format"].(string); ok && !formats[f] {
		delete(s, "format")
	}
	for _, k := range []string{"items", "additionalProperties"} {
		s[k] = strict(s[k])
	}
	// The keys of these are field and type names, not keywords.
	for _, k := range []string{"properties", "$defs"} {
		if m, ok := s[k].(map[string]any); ok {
			for name, c := range m {
				m[name] = strict(c)
			}
		}
	}
	if _, ok := s["properties"]; ok && s["additionalProperties"] == nil {
		s["additionalProperties"] = false
	}
	for k, c := range s {
		if c == nil {
			delete(s, k)
		}
	}
	return s
}
