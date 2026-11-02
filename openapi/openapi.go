// Package openapi generates an OpenAPI 3.1 document from the route table.
// Because handlers are typed, there is
// nothing to annotate: parameters come from the In struct tags, the request
// body from the Body field, and the response from Out. Route options add the
// rest:
//
//	r = r.Prefix("/users").Use(openapi.Tags("users"))
//	r.Get("/{id}", dygo.Handle(c.Get), openapi.Summary("Get a user"), openapi.Response(404, "No such user"))
//
// Serve it by importing the module:
//
//	Imports: []*dygo.Module{openapi.Module(openapi.Info{Title: "Users API", Version: "1.0"})}
//
// which adds GET /openapi.json and a Swagger UI at GET /docs.
//
// Struct tags understood on fields: json, the binding tags (path, query,
// header, cookie, form, file, body, default, required), validate (required,
// min, max, gte, lte, gt, lt, len, email, url, uuid, oneof), description and
// example.
package openapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
)

// Info describes the API.
type Info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// Options configure Module and Generate.
type Options struct {
	// Info defaults to title "API", version "1.0".
	Info Info
	// Servers lists base URLs, such as "https://api.example.com".
	Servers []string
	// BearerAuth adds an HTTP bearer security scheme required by every
	// operation not marked Public().
	BearerAuth bool
	// JSONPath defaults to "/openapi.json"; DocsPath to "/docs". Set DocsPath
	// to "-" to serve no UI.
	JSONPath, DocsPath string
	// SwaggerUIURL is the base of the swagger-ui-dist files. Defaults to a
	// pinned jsDelivr URL.
	SwaggerUIURL string
	// Use adds route options to the document and UI routes, for example your
	// auth guard's "public" marker.
	Use []dygo.RouteOption
}

type (
	summaryKey     struct{}
	descriptionKey struct{}
	tagsKey        struct{}
	deprecatedKey  struct{}
	operationIDKey struct{}
	responsesKey   struct{}
	excludeKey     struct{}
	publicKey      struct{}
)

// Summary sets the operation summary (@ApiOperation({summary})).
func Summary(s string) dygo.RouteOption { return dygo.SetMetadata(summaryKey{}, s) }

// Description sets the operation description.
func Description(s string) dygo.RouteOption { return dygo.SetMetadata(descriptionKey{}, s) }

// Tags groups operations (@ApiTags). Controller and route tags are merged.
func Tags(t ...string) dygo.RouteOption { return dygo.SetMetadata(tagsKey{}, t) }

// Deprecated marks the operation deprecated.
func Deprecated() dygo.RouteOption { return dygo.SetMetadata(deprecatedKey{}, true) }

// OperationID overrides the generated operation ID (Controller_Handler).
func OperationID(id string) dygo.RouteOption { return dygo.SetMetadata(operationIDKey{}, id) }

// Exclude leaves the route out of the document (@ApiExcludeEndpoint).
func Exclude() dygo.RouteOption { return dygo.SetMetadata(excludeKey{}, true) }

// Public marks a route that needs no authentication when BearerAuth is on.
func Public() dygo.RouteOption { return dygo.SetMetadata(publicKey{}, true) }

// ResponseDoc documents an extra response.
type ResponseDoc struct {
	Status      int
	Description string
	// Type is the body type; nil means the standard error body.
	Type reflect.Type
}

// Response documents an error response with the standard error body
// (@ApiResponse). Controller and route responses are merged.
func Response(status int, description string) dygo.RouteOption {
	return dygo.SetMetadata(responsesKey{}, []ResponseDoc{{Status: status, Description: description}})
}

// ResponseOf documents a response whose body is T.
func ResponseOf[T any](status int, description string) dygo.RouteOption {
	return dygo.SetMetadata(responsesKey{}, []ResponseDoc{{Status: status, Description: description, Type: reflect.TypeFor[T]()}})
}

// Module serves the document and the UI. The routes are version neutral and
// excluded from the document; exclude their paths from a global prefix if
// you want them at the root.
func Module(o Options) *dygo.Module {
	o = defaults(o)
	return &dygo.Module{
		Name:        "openapi",
		Controllers: []dygo.Provider{dygo.Value(&controller{opts: o})},
	}
}

func defaults(o Options) Options {
	if o.Info.Title == "" {
		o.Info.Title = "API"
	}
	if o.Info.Version == "" {
		o.Info.Version = "1.0"
	}
	if o.JSONPath == "" {
		o.JSONPath = "/openapi.json"
	}
	if o.DocsPath == "" {
		o.DocsPath = "/docs"
	}
	if o.SwaggerUIURL == "" {
		o.SwaggerUIURL = "https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14"
	}
	return o
}

type controller struct {
	opts Options
	once sync.Once
	doc  []byte
	err  error
}

func (c *controller) Routes(r dygo.Router) {
	r = r.Use(dygo.Version(dygo.VersionNeutral), Exclude()).Use(c.opts.Use...)
	r.Get(c.opts.JSONPath, dygo.HandleRaw(c.serveJSON))
	if c.opts.DocsPath != "-" {
		r.Get(c.opts.DocsPath, dygo.HandleRaw(c.serveDocs))
	}
}

func (c *controller) serveJSON(w http.ResponseWriter, r *http.Request) error {
	// The route table is complete once the app serves, so build on first use.
	c.once.Do(func() {
		c.doc, c.err = json.MarshalIndent(Generate(dygo.RoutesOf(r.Context()), c.opts), "", "  ")
	})
	if c.err != nil {
		return errs.Internal("", errs.Cause(c.err))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write(c.doc)
	return err
}

func (c *controller) serveDocs(w http.ResponseWriter, r *http.Request) error {
	jsonURL := c.opts.JSONPath
	if p := dygo.RouteOf(r.Context()).Path; strings.HasSuffix(p, c.opts.DocsPath) {
		jsonURL = strings.TrimSuffix(p, c.opts.DocsPath) + c.opts.JSONPath // keep a global prefix
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; "+
		"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; img-src 'self' data: https:; connect-src 'self'")
	_, err := strings.NewReplacer("{{ui}}", c.opts.SwaggerUIURL, "{{url}}", jsonURL, "{{title}}", c.opts.Info.Title).
		WriteString(w, docsPage)
	return err
}

const docsPage = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{title}}</title><link rel="stylesheet" href="{{ui}}/swagger-ui.css"></head>
<body><div id="ui"></div><script src="{{ui}}/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url: "{{url}}", dom_id: "#ui"});</script></body></html>
`

// Generate builds the document for routes, usually app.Routes().
func Generate(routes []dygo.RouteInfo, o Options) *Document {
	o = defaults(o)
	g := &generator{schemas: map[string]*Schema{}, names: map[reflect.Type]string{}}
	doc := &Document{OpenAPI: "3.1.0", Info: o.Info, Paths: map[string]*PathItem{}}
	for _, s := range o.Servers {
		doc.Servers = append(doc.Servers, Server{URL: s})
	}
	if o.BearerAuth {
		doc.Components.SecuritySchemes = map[string]*SecurityScheme{"bearer": {Type: "http", Scheme: "bearer"}}
		doc.Security = []map[string][]string{{"bearer": {}}}
	}
	ids := map[string]int{}
	for _, ri := range routes {
		if ex, _ := ri.Metadata(excludeKey{}); ex == true {
			continue
		}
		methods := []string{ri.Method}
		if ri.Method == "" {
			methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
		}
		path := docPath(ri.Path)
		item := doc.Paths[path]
		if item == nil {
			item = &PathItem{}
			doc.Paths[path] = item
		}
		for _, m := range methods {
			op := g.operation(ri, m, o.BearerAuth)
			ids[op.OperationID]++
			if n := ids[op.OperationID]; n > 1 {
				op.OperationID += "_" + strconv.Itoa(n)
			}
			item.set(m, op)
		}
	}
	doc.Components.Schemas = g.schemas
	doc.Components.Schemas["Error"] = errorSchema()
	return doc
}

// docPath turns a ServeMux pattern into an OpenAPI path: "{path...}" becomes
// "{path}" and "{$}" is dropped.
func docPath(p string) string {
	p = strings.ReplaceAll(p, "{$}", "")
	p = strings.ReplaceAll(p, "...}", "}")
	if p == "" {
		p = "/"
	}
	return p
}

func errorSchema() *Schema {
	return &Schema{Type: "object", Required: []string{"statusCode", "error", "message"}, Properties: map[string]*Schema{
		"statusCode": {Type: "integer"},
		"error":      {Type: "string"},
		"message":    {Type: "string"},
		"code":       {Type: "string"},
		"details":    {},
	}}
}

var (
	noContentType = reflect.TypeFor[dygo.NoContent]()
	responderType = reflect.TypeFor[dygo.Responder]()
)

func (g *generator) operation(ri dygo.RouteInfo, method string, bearer bool) *Operation {
	op := &Operation{Responses: map[string]*ResponseObj{}}
	op.OperationID = strings.TrimPrefix(lastPart(ri.Controller), "*") + "_" + lastPart(ri.Handler)
	if ri.Version != "" && ri.Version != dygo.VersionNeutral {
		op.OperationID += "_v" + ri.Version
	}
	if v, ok := ri.Metadata(operationIDKey{}); ok {
		op.OperationID = v.(string)
	}
	if v, ok := ri.Metadata(summaryKey{}); ok {
		op.Summary = v.(string)
	}
	if v, ok := ri.Metadata(descriptionKey{}); ok {
		op.Description = v.(string)
	}
	if v, ok := ri.Metadata(tagsKey{}); ok {
		op.Tags = slices.Compact(slices.Clone(v.([]string)))
	}
	if v, _ := ri.Metadata(deprecatedKey{}); v == true {
		op.Deprecated = true
	}
	if public, _ := ri.Metadata(publicKey{}); bearer && public == true {
		op.Security = []map[string][]string{} // no security for this operation
	}

	if ri.Input != nil {
		g.inputs(op, ri.Input)
	}
	status := ri.Status
	if status == 0 {
		status = http.StatusOK
		if method == http.MethodPost {
			status = http.StatusCreated
		}
	}
	out := ri.Output
	switch {
	case out == nil || out.Implements(responderType):
		op.Responses[strconv.Itoa(status)] = &ResponseObj{Description: http.StatusText(status)}
	case out == noContentType:
		op.Responses["204"] = &ResponseObj{Description: "No Content"}
	case out.Kind() == reflect.String:
		op.Responses[strconv.Itoa(status)] = &ResponseObj{
			Description: http.StatusText(status),
			Content:     map[string]MediaType{"text/plain": {Schema: &Schema{Type: "string"}}},
		}
	case out.Kind() == reflect.Slice && out.Elem().Kind() == reflect.Uint8:
		op.Responses[strconv.Itoa(status)] = &ResponseObj{
			Description: http.StatusText(status),
			Content:     map[string]MediaType{"application/octet-stream": {Schema: &Schema{Type: "string", Format: "binary"}}},
		}
	default:
		op.Responses[strconv.Itoa(status)] = &ResponseObj{
			Description: http.StatusText(status),
			Content:     map[string]MediaType{"application/json": {Schema: g.schema(out)}},
		}
	}
	if len(op.Parameters) > 0 || op.RequestBody != nil {
		op.Responses["400"] = errResponse("Invalid request")
	}
	if v, ok := ri.Metadata(responsesKey{}); ok {
		for _, rd := range v.([]ResponseDoc) {
			r := errResponse(rd.Description)
			if rd.Type != nil {
				r.Content = map[string]MediaType{"application/json": {Schema: g.schema(rd.Type)}}
			}
			op.Responses[strconv.Itoa(rd.Status)] = r
		}
	}
	return op
}

func errResponse(desc string) *ResponseObj {
	return &ResponseObj{Description: desc, Content: map[string]MediaType{"application/json": {Schema: &Schema{Ref: "#/components/schemas/Error"}}}}
}

func lastPart(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

var sources = []string{"path", "query", "header", "cookie"}

// inputs adds parameters and the request body from an In struct.
func (g *generator) inputs(op *Operation, t reflect.Type) {
	var form *Schema
	multipart := false
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		if sf.Anonymous && sf.Type.Kind() == reflect.Struct && !hasSource(sf.Tag) {
			g.inputs(op, sf.Type)
			continue
		}
		for _, src := range sources {
			name, ok := sf.Tag.Lookup(src)
			if !ok {
				continue
			}
			p := &Parameter{
				Name: name, In: src, Required: src == "path" || sf.Tag.Get("required") == "true",
				Description: sf.Tag.Get("description"), Schema: g.fieldSchema(sf),
			}
			if p.Schema.Description == p.Description {
				p.Schema.Description = ""
			}
			if sf.Type.Kind() == reflect.Slice && src == "query" {
				p.Explode = true
			}
			if p.Schema.Default != nil || sf.Tag.Get("default") != "" {
				p.Required = src == "path"
			}
			op.Parameters = append(op.Parameters, p)
		}
		if name, ok := sf.Tag.Lookup("form"); ok {
			if form == nil {
				form = &Schema{Type: "object", Properties: map[string]*Schema{}}
			}
			form.Properties[name] = g.fieldSchema(sf)
			if sf.Tag.Get("required") == "true" {
				form.Required = append(form.Required, name)
			}
		}
		if name, ok := sf.Tag.Lookup("file"); ok {
			if form == nil {
				form = &Schema{Type: "object", Properties: map[string]*Schema{}}
			}
			multipart = true
			s := &Schema{Type: "string", Format: "binary"}
			if sf.Type.Kind() == reflect.Slice {
				s = &Schema{Type: "array", Items: s}
			}
			form.Properties[name] = s
		}
		if kind, ok := sf.Tag.Lookup("body"); ok {
			rb := &RequestBody{Required: true, Content: map[string]MediaType{}}
			switch kind {
			case "raw":
				rb.Content["application/octet-stream"] = MediaType{Schema: &Schema{Type: "string", Format: "binary"}}
			case "form":
				rb.Content["application/x-www-form-urlencoded"] = MediaType{Schema: g.schema(sf.Type)}
			default:
				rb.Content["application/json"] = MediaType{Schema: g.schema(sf.Type)}
			}
			op.RequestBody = rb
		}
	}
	if form != nil && op.RequestBody == nil {
		ct := "application/x-www-form-urlencoded"
		if multipart {
			ct = "multipart/form-data"
		}
		op.RequestBody = &RequestBody{Required: true, Content: map[string]MediaType{ct: {Schema: form}}}
	}
}

func hasSource(tag reflect.StructTag) bool {
	for _, s := range []string{"path", "query", "header", "cookie", "form", "file", "body"} {
		if _, ok := tag.Lookup(s); ok {
			return true
		}
	}
	return false
}
