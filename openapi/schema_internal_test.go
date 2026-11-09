package openapi

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

// jsonType marshals itself, so its schema is left open.
type jsonType struct{}

func (jsonType) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

func TestSchemaKinds(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
		want Schema
	}{
		{"time", reflect.TypeFor[time.Time](), Schema{Type: "string", Format: "date-time"}},
		{"duration", reflect.TypeFor[time.Duration](), Schema{Type: "integer", Format: "int64", Description: "nanoseconds"}},
		{"raw json", reflect.TypeFor[json.RawMessage](), Schema{}},
		{"json marshaler", reflect.TypeFor[jsonType](), Schema{}},
		{"text marshaler", reflect.TypeFor[netip.Addr](), Schema{Type: "string"}},
		{"bool", reflect.TypeFor[bool](), Schema{Type: "boolean"}},
		{"int16", reflect.TypeFor[int16](), Schema{Type: "integer", Format: "int32"}},
		{"float32", reflect.TypeFor[float32](), Schema{Type: "number", Format: "float"}},
		{"float64 pointer", reflect.TypeFor[*float64](), Schema{Type: "number", Format: "double"}},
		{"bytes", reflect.TypeFor[[]byte](), Schema{Type: "string", Format: "byte"}},
		{"map", reflect.TypeFor[map[string]bool](), Schema{Type: "object", AdditionalProperties: &Schema{Type: "boolean"}}},
		{"func", reflect.TypeFor[func()](), Schema{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &generator{schemas: map[string]*Schema{}, names: map[reflect.Type]string{}}
			if got := g.schema(tt.typ); !reflect.DeepEqual(*got, tt.want) {
				t.Fatalf("got %+v, want %+v", *got, tt.want)
			}
		})
	}
}

type Inner struct {
	A string `json:"a"`
}

type outer struct {
	*Inner
	Skip   string `json:"-"`
	NoTag  int
	AsText int    `json:"as_text,string"`
	Ref    Inner  `json:"ref" description:"the inner one"`
	hidden string //nolint:unused // unexported fields are skipped
}

func TestFields(t *testing.T) {
	g := &generator{schemas: map[string]*Schema{}, names: map[reflect.Type]string{}}
	s := g.object(reflect.TypeFor[outer]())
	for _, name := range []string{"a", "NoTag", "as_text", "ref"} {
		if s.Properties[name] == nil {
			t.Errorf("no property %q in %v", name, s.Properties)
		}
	}
	if s.Properties["Skip"] != nil || s.Properties["hidden"] != nil {
		t.Errorf("skipped fields listed: %v", s.Properties)
	}
	if s.Properties["as_text"].Type != "string" {
		t.Errorf("as_text = %+v", s.Properties["as_text"])
	}
	if r := s.Properties["ref"]; r.Ref == "" || r.Description != "the inner one" {
		t.Errorf("ref = %+v", r)
	}
}

func TestUniqueName(t *testing.T) {
	g := &generator{schemas: map[string]*Schema{"Inner": {}}, names: map[reflect.Type]string{}}
	if got := g.uniqueName(reflect.TypeFor[Inner]()); got != "Inner2" {
		t.Fatalf("uniqueName = %q", got)
	}
}

func ptr[T any](v T) *T { return &v }

func TestApplyRules(t *testing.T) {
	tests := []struct {
		name  string
		typ   string
		rules string
		want  Schema
	}{
		{"number range", "number", "gte=1,lte=9", Schema{Type: "number", Minimum: ptr(1.0), Maximum: ptr(9.0)}},
		{"exclusive", "integer", "gt=0,lt=10", Schema{Type: "integer", ExclusiveMinimum: ptr(0.0), ExclusiveMaximum: ptr(10.0)}},
		{"exclusive ignored on strings", "string", "gt=0,lt=10", Schema{Type: "string"}},
		{"array size", "array", "min=1,max=5", Schema{Type: "array", MinItems: ptr(1), MaxItems: ptr(5)}},
		{"string len", "string", "len=4", Schema{Type: "string", MinLength: ptr(4), MaxLength: ptr(4)}},
		{"array len", "array", "len=2", Schema{Type: "array", MinItems: ptr(2), MaxItems: ptr(2)}},
		{"bad numbers", "string", "min=x,max=y", Schema{Type: "string"}},
		{"formats", "string", "url", Schema{Type: "string", Format: "uri"}},
		{"uuid", "string", "uuid4", Schema{Type: "string", Format: "uuid"}},
		{"datetime", "string", "datetime", Schema{Type: "string", Format: "date-time"}},
		{"dive stops", "array", "min=1,dive,max=3", Schema{Type: "array", MinItems: ptr(1)}},
		{"oneof numbers", "number", "oneof=1.5 2", Schema{Type: "number", Enum: []any{1.5, 2.0}}},
		{"oneof bools", "boolean", "oneof=true false", Schema{Type: "boolean", Enum: []any{true, false}}},
		{"oneof bad bool", "boolean", "oneof=maybe", Schema{Type: "boolean", Enum: []any{"maybe"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Schema{Type: tt.typ}
			applyRules(s, tt.rules)
			if !reflect.DeepEqual(*s, tt.want) {
				got, _ := json.Marshal(s)
				want, _ := json.Marshal(tt.want)
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

type jsNode struct {
	Name     string    `json:"name" validate:"required,max=20"`
	Children []*jsNode `json:"children,omitempty"`
}

type jsLeaf struct {
	ID int `json:"id"`
}

type jsRoot struct {
	Title string   `json:"title"`
	Leaf  jsLeaf   `json:"leaf"`
	Tags  []string `json:"tags,omitempty"`
}

func TestJSONSchema(t *testing.T) {
	s := JSONSchema(reflect.TypeFor[jsRoot]())
	if s.Type != "object" || s.Ref != "" {
		t.Fatalf("root should be inlined: %+v", s)
	}
	if s.Properties["leaf"].Ref != "#/$defs/jsLeaf" || s.Defs["jsLeaf"] == nil {
		t.Fatalf("leaf should be a $def: %+v", s.Properties["leaf"])
	}
	if _, ok := s.Defs["jsRoot"]; ok {
		t.Fatal("unreferenced root kept in $defs")
	}

	rec := JSONSchema(reflect.TypeFor[*jsNode]())
	if rec.Defs["jsNode"] == nil || rec.Defs["jsNode"].Defs != nil {
		t.Fatalf("recursive root must stay in $defs, without its own $defs: %+v", rec.Defs)
	}
	if *rec.Properties["name"].MaxLength != 20 {
		t.Fatal("validate tags not applied")
	}
	if _, err := json.Marshal(rec); err != nil {
		t.Fatal(err)
	}

	if s := JSONSchema(reflect.TypeFor[map[string]int]()); s.Type != "object" || s.Defs != nil {
		t.Fatalf("map: %+v", s)
	}
}
