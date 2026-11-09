package openapi

import (
	"encoding"
	"encoding/json"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Document is an OpenAPI 3.1 document.
type Document struct {
	OpenAPI    string                `json:"openapi"`
	Info       Info                  `json:"info"`
	Servers    []Server              `json:"servers,omitempty"`
	Paths      map[string]*PathItem  `json:"paths"`
	Components Components            `json:"components"`
	Security   []map[string][]string `json:"security,omitempty"`
}

// Server is a base URL.
type Server struct {
	URL string `json:"url"`
}

// Components holds shared schemas.
type Components struct {
	Schemas         map[string]*Schema         `json:"schemas,omitempty"`
	SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty"`
}

// SecurityScheme is an authentication method.
type SecurityScheme struct {
	Type   string `json:"type"`
	Scheme string `json:"scheme,omitempty"`
}

// PathItem holds the operations of one path.
type PathItem struct {
	Get     *Operation `json:"get,omitempty"`
	Put     *Operation `json:"put,omitempty"`
	Post    *Operation `json:"post,omitempty"`
	Delete  *Operation `json:"delete,omitempty"`
	Options *Operation `json:"options,omitempty"`
	Head    *Operation `json:"head,omitempty"`
	Patch   *Operation `json:"patch,omitempty"`
}

func (p *PathItem) set(method string, op *Operation) {
	switch method {
	case "GET":
		p.Get = op
	case "PUT":
		p.Put = op
	case "POST":
		p.Post = op
	case "DELETE":
		p.Delete = op
	case "OPTIONS":
		p.Options = op
	case "HEAD":
		p.Head = op
	case "PATCH":
		p.Patch = op
	}
}

// Operation is one route.
type Operation struct {
	OperationID string                  `json:"operationId"`
	Summary     string                  `json:"summary,omitempty"`
	Description string                  `json:"description,omitempty"`
	Tags        []string                `json:"tags,omitempty"`
	Deprecated  bool                    `json:"deprecated,omitempty"`
	Parameters  []*Parameter            `json:"parameters,omitempty"`
	RequestBody *RequestBody            `json:"requestBody,omitempty"`
	Responses   map[string]*ResponseObj `json:"responses"`
	Security    []map[string][]string   `json:"security,omitempty"`
}

// MarshalJSON keeps an empty Security list, which means "no auth".
func (o *Operation) MarshalJSON() ([]byte, error) {
	type plain Operation
	if o.Security != nil && len(o.Security) == 0 {
		return json.Marshal(struct {
			*plain
			Security []map[string][]string `json:"security"`
		}{(*plain)(o), o.Security})
	}
	return json.Marshal((*plain)(o))
}

// Parameter is a path, query, header or cookie input.
type Parameter struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Required    bool    `json:"required,omitempty"`
	Description string  `json:"description,omitempty"`
	Explode     bool    `json:"explode,omitempty"`
	Schema      *Schema `json:"schema"`
}

// RequestBody is the body input.
type RequestBody struct {
	Required bool                 `json:"required,omitempty"`
	Content  map[string]MediaType `json:"content"`
}

// ResponseObj is one response.
type ResponseObj struct {
	Description string               `json:"description"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// MediaType holds a body schema.
type MediaType struct {
	Schema *Schema `json:"schema,omitempty"`
}

// Schema is a JSON Schema (the subset Dygo generates).
type Schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Defs                 map[string]*Schema `json:"$defs,omitempty"`
	Type                 any                `json:"type,omitempty"` // string, or []string with "null"
	Format               string             `json:"format,omitempty"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	AdditionalProperties *Schema            `json:"additionalProperties,omitempty"`
	Enum                 []any              `json:"enum,omitempty"`
	Default              any                `json:"default,omitempty"`
	Examples             []any              `json:"examples,omitempty"`
	Minimum              *float64           `json:"minimum,omitempty"`
	Maximum              *float64           `json:"maximum,omitempty"`
	ExclusiveMinimum     *float64           `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum     *float64           `json:"exclusiveMaximum,omitempty"`
	MinLength            *int               `json:"minLength,omitempty"`
	MaxLength            *int               `json:"maxLength,omitempty"`
	MinItems             *int               `json:"minItems,omitempty"`
	MaxItems             *int               `json:"maxItems,omitempty"`
}

type generator struct {
	schemas map[string]*Schema
	names   map[reflect.Type]string
	ref     string // prefix of $ref values; empty means components
}

// refPrefix is where $refs point: the document's components, or $defs.
func (g *generator) refPrefix() string {
	if g.ref == "" {
		return "#/components/schemas/"
	}
	return g.ref
}

// JSONSchema returns a self-contained JSON Schema for t, built the same way
// as the schemas of a document: json tags give the names, validate tags give
// limits and required fields. Named structs other than t itself go in $defs.
// The ai package uses it to ask a model for typed output.
func JSONSchema(t reflect.Type) *Schema {
	const prefix = "#/$defs/"
	g := &generator{schemas: map[string]*Schema{}, names: map[reflect.Type]string{}, ref: prefix}
	root := g.schema(t)
	if name, ok := strings.CutPrefix(root.Ref, prefix); ok {
		cp := *g.schemas[name] // a copy: the root gets $defs, the def must not
		root = &cp
		if !refers(g.schemas, prefix+name) {
			delete(g.schemas, name)
		}
	}
	if len(g.schemas) > 0 {
		root.Defs = g.schemas
	}
	return root
}

// refers reports whether any schema in defs holds the $ref ref.
func refers(defs map[string]*Schema, ref string) bool {
	var walk func(*Schema) bool
	walk = func(s *Schema) bool {
		if s == nil {
			return false
		}
		if s.Ref == ref || walk(s.Items) || walk(s.AdditionalProperties) {
			return true
		}
		for _, p := range s.Properties {
			if walk(p) {
				return true
			}
		}
		return false
	}
	for _, s := range defs {
		if walk(s) {
			return true
		}
	}
	return false
}

var (
	timeType      = reflect.TypeFor[time.Time]()
	durationType  = reflect.TypeFor[time.Duration]()
	textMarshaler = reflect.TypeFor[encoding.TextMarshaler]()
	jsonMarshaler = reflect.TypeFor[json.Marshaler]()
	rawMessage    = reflect.TypeFor[json.RawMessage]()
)

// schema returns the schema for t; named structs become $refs to components.
func (g *generator) schema(t reflect.Type) *Schema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == timeType:
		return &Schema{Type: "string", Format: "date-time"}
	case t == durationType:
		return &Schema{Type: "integer", Format: "int64", Description: "nanoseconds"}
	case t == rawMessage:
		return &Schema{}
	case t.Implements(jsonMarshaler) || reflect.PointerTo(t).Implements(jsonMarshaler):
		return &Schema{}
	case t.Implements(textMarshaler) || reflect.PointerTo(t).Implements(textMarshaler):
		return &Schema{Type: "string"}
	}
	switch t.Kind() {
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Int, reflect.Int64, reflect.Uint, reflect.Uint64, reflect.Uintptr:
		return &Schema{Type: "integer", Format: "int64"}
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return &Schema{Type: "integer", Format: "int32"}
	case reflect.Float32:
		return &Schema{Type: "number", Format: "float"}
	case reflect.Float64:
		return &Schema{Type: "number", Format: "double"}
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return &Schema{Type: "string", Format: "byte"}
		}
		return &Schema{Type: "array", Items: g.schema(t.Elem())}
	case reflect.Map:
		return &Schema{Type: "object", AdditionalProperties: g.schema(t.Elem())}
	case reflect.Struct:
		if t.Name() == "" {
			return g.object(t)
		}
		name, ok := g.names[t]
		if !ok {
			name = g.uniqueName(t)
			g.names[t] = name
			g.schemas[name] = &Schema{} // placeholder for recursive types
			*g.schemas[name] = *g.object(t)
		}
		return &Schema{Ref: g.refPrefix() + name}
	}
	return &Schema{}
}

var nonName = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func (g *generator) uniqueName(t reflect.Type) string {
	name := t.Name()
	// Generic instances: "Page[example.com/users.User]" becomes "Page_User".
	if i := strings.IndexByte(name, '['); i >= 0 {
		args := strings.Split(strings.TrimSuffix(name[i+1:], "]"), ",")
		for j, a := range args {
			args[j] = lastPart(strings.TrimPrefix(strings.TrimSpace(a), "*"))
		}
		name = name[:i] + "_" + strings.Join(args, "_")
	}
	name = nonName.ReplaceAllString(name, "_")
	base := name
	for n := 2; ; n++ {
		if _, taken := g.schemas[name]; !taken {
			return name
		}
		name = base + strconv.Itoa(n)
	}
}

// object describes a struct's JSON shape, following encoding/json rules.
func (g *generator) object(t reflect.Type) *Schema {
	s := &Schema{Type: "object", Properties: map[string]*Schema{}}
	g.fields(t, s)
	return s
}

func (g *generator) fields(t reflect.Type, s *Schema) {
	for i := range t.NumField() {
		sf := t.Field(i)
		tag := sf.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if sf.Anonymous && name == "" {
			ft := sf.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				g.fields(ft, s)
				continue
			}
		}
		if !sf.IsExported() {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		fs := g.fieldSchema(sf)
		if strings.Contains(opts, "string") {
			fs = &Schema{Type: "string", Description: fs.Description}
		}
		s.Properties[name] = fs
		omit := strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero")
		if !omit && sf.Type.Kind() != reflect.Pointer || strings.Contains(sf.Tag.Get("validate"), "required") {
			s.Required = append(s.Required, name)
		}
	}
}

// fieldSchema is the type's schema plus what the field's tags say.
func (g *generator) fieldSchema(sf reflect.StructField) *Schema {
	base := g.schema(sf.Type)
	d, ex := sf.Tag.Get("description"), sf.Tag.Get("example")
	def, hasDef := sf.Tag.Lookup("default")
	rules := sf.Tag.Get("validate")
	if base.Ref != "" && d == "" && ex == "" && !hasDef && rules == "" {
		return base
	}
	s := base
	if base.Ref != "" {
		s = &Schema{Ref: base.Ref} // 3.1 allows siblings next to $ref
	} else {
		cp := *base
		s = &cp
	}
	if d != "" {
		s.Description = d
	}
	if ex != "" {
		s.Examples = []any{typed(ex, s)}
	}
	if hasDef {
		s.Default = typed(def, s)
	}
	applyRules(s, rules)
	return s
}

// typed turns a tag string into a JSON value matching the schema type.
func typed(v string, s *Schema) any {
	switch s.Type {
	case "integer":
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	case "number":
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	case "boolean":
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return v
}

// applyRules maps go-playground/validator rules to JSON Schema keywords.
func applyRules(s *Schema, rules string) {
	if rules == "" {
		return
	}
	isStr, isArr := s.Type == "string", s.Type == "array"
	for _, r := range strings.Split(rules, ",") {
		if r == "dive" {
			break // the rest applies to elements
		}
		name, param, _ := strings.Cut(r, "=")
		f, numErr := strconv.ParseFloat(param, 64)
		n := int(f)
		switch name {
		case "min", "gte":
			switch {
			case numErr != nil:
			case isStr:
				s.MinLength = &n
			case isArr:
				s.MinItems = &n
			default:
				s.Minimum = &f
			}
		case "max", "lte":
			switch {
			case numErr != nil:
			case isStr:
				s.MaxLength = &n
			case isArr:
				s.MaxItems = &n
			default:
				s.Maximum = &f
			}
		case "gt":
			if numErr == nil && !isStr && !isArr {
				s.ExclusiveMinimum = &f
			}
		case "lt":
			if numErr == nil && !isStr && !isArr {
				s.ExclusiveMaximum = &f
			}
		case "len":
			if numErr == nil && isStr {
				s.MinLength, s.MaxLength = &n, &n
			} else if numErr == nil && isArr {
				s.MinItems, s.MaxItems = &n, &n
			}
		case "email":
			s.Format = "email"
		case "url", "http_url", "uri":
			s.Format = "uri"
		case "uuid", "uuid4":
			s.Format = "uuid"
		case "datetime":
			s.Format = "date-time"
		case "oneof":
			for _, v := range strings.Fields(param) {
				s.Enum = append(s.Enum, typed(v, s))
			}
		}
	}
}
