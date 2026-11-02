// Package binder fills a handler's input struct from an HTTP request using
// struct tags. A Plan is compiled once per input type; Bind then runs a flat
// list of field setters with no tag parsing on the request path.
package binder

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FieldError describes one input value that could not be bound.
type FieldError struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Reason string `json:"reason"`
}

// Error lists every field that failed to bind. It maps to a 400 response.
type Error struct {
	Fields []FieldError
}

func (e *Error) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = fmt.Sprintf("%s %q %s", f.Source, f.Name, f.Reason)
	}
	return "invalid request: " + strings.Join(parts, "; ")
}

// Options change binding per route.
type Options struct {
	// StrictJSON rejects JSON bodies with unknown fields.
	StrictJSON bool
}

// SourceFunc produces a value for a custom source tag.
type SourceFunc func(*http.Request) (any, error)

// setFunc parses s and stores it in dst, which is addressable and settable.
// Setting in place avoids allocating a reflect.Value per conversion.
type setFunc func(s string, dst reflect.Value) error

var (
	registryMu sync.RWMutex
	converters = map[reflect.Type]func(string) (any, error){}
	sources    = map[string]SourceFunc{}
	plans      sync.Map // reflect.Type -> planResult
)

// RegisterConverter makes type t bindable from a string. Register before
// building the application.
func RegisterConverter(t reflect.Type, fn func(string) (any, error)) {
	registryMu.Lock()
	defer registryMu.Unlock()
	converters[t] = fn
	plans.Clear()
}

// RegisterSource adds a struct tag that takes its value from fn. Register
// before building the application.
func RegisterSource(name string, fn SourceFunc) {
	registryMu.Lock()
	defer registryMu.Unlock()
	sources[name] = fn
	plans.Clear()
}

const (
	srcPath   = "path"
	srcQuery  = "query"
	srcHeader = "header"
	srcCookie = "cookie"
	srcForm   = "form"
	srcFile   = "file"
	srcBody   = "body"
)

var builtinSources = []string{srcPath, srcQuery, srcHeader, srcCookie, srcForm, srcFile, srcBody}

type shape int

const (
	shapeScalar shape = iota
	shapePtr
	shapeSlice
	shapeCookie // *http.Cookie
	shapeFile   // *multipart.FileHeader
	shapeFiles  // []*multipart.FileHeader
	shapeCustom // value from a SourceFunc
	shapeJSON   // body:"json"
	shapeForm   // body:"form"
	shapeRaw    // body:"raw"
)

type field struct {
	index    []int
	source   string
	name     string
	shape    shape
	typ      reflect.Type
	set      setFunc
	def      string
	hasDef   bool
	required bool
	custom   SourceFunc
	form     []*field // body:"form" sub fields
}

// Plan binds one input type.
type Plan struct {
	typ       reflect.Type
	fields    []*field
	pathNames []string
	needsForm bool
	multipart bool
}

type planResult struct {
	p   *Plan
	err error
}

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	durationType        = reflect.TypeFor[time.Duration]()
	cookieType          = reflect.TypeFor[*http.Cookie]()
	fileType            = reflect.TypeFor[*multipart.FileHeader]()
	filesType           = reflect.TypeFor[[]*multipart.FileHeader]()
	bytesType           = reflect.TypeFor[[]byte]()
	readerType          = reflect.TypeFor[io.Reader]()
)

// Compile returns the plan for struct type t, cached per type.
func Compile(t reflect.Type) (*Plan, error) {
	if v, ok := plans.Load(t); ok {
		r := v.(planResult)
		return r.p, r.err
	}
	p, err := compile(t)
	plans.Store(t, planResult{p, err})
	return p, err
}

func compile(t reflect.Type) (*Plan, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("input type %s must be a struct", t)
	}
	registryMu.RLock()
	defer registryMu.RUnlock()
	p := &Plan{typ: t}
	var errs []error
	bodies := 0
	walk(t, nil, func(f *field, err error) {
		if err != nil {
			errs = append(errs, err)
			return
		}
		switch f.source {
		case srcPath:
			p.pathNames = append(p.pathNames, f.name)
		case srcForm, srcFile:
			p.needsForm, p.multipart = true, true
		case srcBody:
			bodies++
			if f.shape == shapeForm {
				p.needsForm = true
				for _, sf := range f.form {
					if sf.source == srcFile {
						p.multipart = true
					}
				}
			}
		}
		p.fields = append(p.fields, f)
	}, false)
	if bodies > 1 {
		errs = append(errs, fmt.Errorf("%s has %d body fields; only one is allowed", t, bodies))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("input type %s: %w", t, errors.Join(errs...))
	}
	return p, nil
}

// walk visits tagged fields, flattening untagged embedded structs.
func walk(t reflect.Type, prefix []int, visit func(*field, error), formOnly bool) {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		index := append(slices.Clone(prefix), i)
		src, name, n := sourceTag(sf.Tag)
		if n == 0 {
			if sf.Anonymous && sf.Type.Kind() == reflect.Struct {
				walk(sf.Type, index, visit, formOnly)
			}
			continue
		}
		if n > 1 {
			visit(nil, fmt.Errorf("field %s has more than one source tag", sf.Name))
			continue
		}
		if !sf.IsExported() {
			visit(nil, fmt.Errorf("field %s must be exported to be bound", sf.Name))
			continue
		}
		if formOnly && src != srcForm && src != srcFile {
			visit(nil, fmt.Errorf("field %s: only form and file tags are allowed inside a body:\"form\" struct", sf.Name))
			continue
		}
		f, err := newField(sf, index, src, name)
		visit(f, err)
	}
}

func sourceTag(tag reflect.StructTag) (src, name string, n int) {
	for _, s := range builtinSources {
		if v, ok := tag.Lookup(s); ok {
			src, name, n = s, v, n+1
		}
	}
	for s := range sources {
		if v, ok := tag.Lookup(s); ok {
			src, name, n = s, v, n+1
		}
	}
	return src, name, n
}

func newField(sf reflect.StructField, index []int, src, name string) (*field, error) {
	f := &field{index: index, source: src, name: name, typ: sf.Type, required: sf.Tag.Get("required") == "true"}
	if name == "" && src != srcBody {
		f.name = src
		if isBuiltin(src) {
			return nil, fmt.Errorf("field %s: %s tag needs a name, like %s:\"id\"", sf.Name, src, src)
		}
	}
	if def, ok := sf.Tag.Lookup("default"); ok {
		f.def, f.hasDef = def, true
	}
	t := sf.Type
	switch {
	case src == srcBody:
		switch name {
		case "json", "":
			f.shape = shapeJSON
		case "form":
			if t.Kind() != reflect.Struct {
				return nil, fmt.Errorf("field %s: body:\"form\" needs a struct", sf.Name)
			}
			f.shape = shapeForm
			var errs []error
			walk(t, nil, func(sub *field, err error) {
				if err != nil {
					errs = append(errs, err)
					return
				}
				f.form = append(f.form, sub)
			}, true)
			if len(errs) > 0 {
				return nil, errors.Join(errs...)
			}
		case "raw":
			if t != bytesType && t.Kind() != reflect.String && t != readerType {
				return nil, fmt.Errorf("field %s: body:\"raw\" needs []byte, string or io.Reader, got %s", sf.Name, t)
			}
			f.shape = shapeRaw
		default:
			return nil, fmt.Errorf("field %s: unknown body kind %q (use json, form or raw)", sf.Name, name)
		}
		return f, nil
	case !isBuiltin(src):
		f.custom = sources[src] // compile holds registryMu
		f.shape = shapeCustom
		return f, nil
	case src == srcFile:
		switch t {
		case fileType:
			f.shape = shapeFile
		case filesType:
			f.shape = shapeFiles
		default:
			return nil, fmt.Errorf("field %s: file tag needs *multipart.FileHeader or []*multipart.FileHeader, got %s", sf.Name, t)
		}
		return f, nil
	case src == srcCookie && t == cookieType:
		f.shape = shapeCookie
		return f, nil
	}
	elem := t
	switch {
	case hasConverter(t):
		f.shape = shapeScalar
	case t.Kind() == reflect.Pointer:
		f.shape, elem = shapePtr, t.Elem()
	case t.Kind() == reflect.Slice:
		f.shape, elem = shapeSlice, t.Elem()
		if src == srcPath {
			return nil, fmt.Errorf("field %s: path parameters cannot be slices", sf.Name)
		}
	default:
		f.shape = shapeScalar
	}
	set, err := setterFor(elem)
	if err != nil {
		return nil, fmt.Errorf("field %s: %w", sf.Name, err)
	}
	f.set = set
	if f.hasDef {
		if err := set(f.def, reflect.New(elem).Elem()); err != nil {
			return nil, fmt.Errorf("field %s: default %q: %v", sf.Name, f.def, err)
		}
	}
	return f, nil
}

func isBuiltin(src string) bool { return slices.Contains(builtinSources, src) }

func hasConverter(t reflect.Type) bool {
	if _, ok := converters[t]; ok {
		return true
	}
	return t == durationType || reflect.PointerTo(t).Implements(textUnmarshalerType)
}

// setterFor returns a string setter for t. The caller holds registryMu.
func setterFor(t reflect.Type) (setFunc, error) {
	if fn, ok := converters[t]; ok {
		return func(s string, dst reflect.Value) error {
			v, err := fn(s)
			if err != nil {
				return err
			}
			dst.Set(reflect.ValueOf(v).Convert(t))
			return nil
		}, nil
	}
	if t == durationType {
		return func(s string, dst reflect.Value) error {
			d, err := time.ParseDuration(s)
			if err != nil {
				return errors.New("must be a duration like 1m30s")
			}
			dst.SetInt(int64(d))
			return nil
		}, nil
	}
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return func(s string, dst reflect.Value) error {
			return dst.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(s))
		}, nil
	}
	switch t.Kind() {
	case reflect.String:
		return func(s string, dst reflect.Value) error { dst.SetString(s); return nil }, nil
	case reflect.Bool:
		return func(s string, dst reflect.Value) error {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return errors.New("must be true or false")
			}
			dst.SetBool(b)
			return nil
		}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		bits := t.Bits()
		msg := fmt.Sprintf("must be an integer (%d bit)", bits)
		return func(s string, dst reflect.Value) error {
			n, err := strconv.ParseInt(s, 10, bits)
			if err != nil {
				return errors.New(msg)
			}
			dst.SetInt(n)
			return nil
		}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		bits := t.Bits()
		msg := fmt.Sprintf("must be a non-negative integer (%d bit)", bits)
		return func(s string, dst reflect.Value) error {
			n, err := strconv.ParseUint(s, 10, bits)
			if err != nil {
				return errors.New(msg)
			}
			dst.SetUint(n)
			return nil
		}, nil
	case reflect.Float32, reflect.Float64:
		bits := t.Bits()
		return func(s string, dst reflect.Value) error {
			n, err := strconv.ParseFloat(s, bits)
			if err != nil {
				return errors.New("must be a number")
			}
			dst.SetFloat(n)
			return nil
		}, nil
	}
	return nil, fmt.Errorf("type %s cannot be bound from a string; register a converter with dygo.RegisterConverter", t)
}

// PathNames returns the path parameter names the plan reads.
func (p *Plan) PathNames() []string { return p.pathNames }

// UsesCustomSources reports whether a field is filled by a RegisterSource
// function, which may read the request's context.
func (p *Plan) UsesCustomSources() bool {
	for _, f := range p.fields {
		if f.custom != nil {
			return true
		}
	}
	return false
}

// Bind fills dst, which must be a settable value of the plan's type. A body
// over the route's size limit returns the *http.MaxBytesError; every other
// problem is collected into one *Error.
func (p *Plan) Bind(r *http.Request, dst reflect.Value, o Options) error {
	if p.needsForm {
		if err := parseForm(r, p.multipart); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return mbe
			}
			return &Error{Fields: []FieldError{{Source: srcBody, Name: "form", Reason: err.Error()}}}
		}
	}
	var errs []FieldError
	var query map[string][]string
	for _, f := range p.fields {
		if f.source == srcQuery && query == nil {
			query = r.URL.Query()
		}
		if err := p.bindField(r, dst, f, query, o, &errs); err != nil {
			return err
		}
	}
	if len(errs) > 0 {
		return &Error{Fields: errs}
	}
	return nil
}

func parseForm(r *http.Request, multipartAllowed bool) error {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if multipartAllowed && ct == "multipart/form-data" {
		return r.ParseMultipartForm(32 << 20)
	}
	return r.ParseForm()
}

func (p *Plan) bindField(r *http.Request, dst reflect.Value, f *field, query map[string][]string, o Options, errs *[]FieldError) error {
	fv := dst.Field(f.index[0])
	if len(f.index) > 1 {
		fv = dst.FieldByIndex(f.index)
	}
	switch f.shape {
	case shapeJSON:
		return bindJSON(r, fv, o, errs)
	case shapeForm:
		for _, sf := range f.form {
			if err := p.bindField(r, fv, sf, query, o, errs); err != nil {
				return err
			}
		}
		return nil
	case shapeRaw:
		return bindRaw(r, fv, errs)
	case shapeCustom:
		if f.custom == nil {
			p.fail(errs, f, "", "has no registered source")
			return nil
		}
		v, err := f.custom(r)
		if err != nil {
			p.fail(errs, f, "", err.Error())
			return nil
		}
		if v == nil {
			return nil
		}
		rv := reflect.ValueOf(v)
		if !rv.Type().AssignableTo(fv.Type()) {
			p.fail(errs, f, "", fmt.Sprintf("source returned %s, not %s", rv.Type(), fv.Type()))
			return nil
		}
		fv.Set(rv)
		return nil
	case shapeCookie:
		c, err := r.Cookie(f.name)
		if err == nil {
			fv.Set(reflect.ValueOf(c))
		} else if f.required {
			p.fail(errs, f, "", "is required")
		}
		return nil
	case shapeFile, shapeFiles:
		var files []*multipart.FileHeader
		if r.MultipartForm != nil {
			files = r.MultipartForm.File[f.name]
		}
		switch {
		case len(files) == 0:
			if f.required {
				p.fail(errs, f, "", "is required")
			}
		case f.shape == shapeFile:
			fv.Set(reflect.ValueOf(files[0]))
		default:
			fv.Set(reflect.ValueOf(files))
		}
		return nil
	}

	// single holds a lone value so the common path/header case allocates no slice.
	var single string
	var values []string
	switch f.source {
	case srcPath:
		single = r.PathValue(f.name)
	case srcQuery:
		values = query[f.name]
	case srcHeader:
		values = r.Header.Values(f.name)
	case srcCookie:
		if c, err := r.Cookie(f.name); err == nil {
			single = c.Value
		}
	case srcForm:
		values = r.PostForm[f.name]
	}
	if single == "" && len(values) > 0 && f.shape != shapeSlice {
		single = values[0]
	}
	if single == "" && len(values) == 0 {
		switch {
		case f.hasDef:
			single = f.def
		case f.required || f.source == srcPath:
			p.fail(errs, f, "", "is required")
			return nil
		default:
			return nil
		}
	}
	switch f.shape {
	case shapeSlice:
		if len(values) == 0 {
			values = []string{single}
		}
		sl := reflect.MakeSlice(f.typ, len(values), len(values))
		for i, raw := range values {
			if err := f.set(raw, sl.Index(i)); err != nil {
				p.fail(errs, f, raw, err.Error())
				return nil
			}
		}
		fv.Set(sl)
	case shapePtr:
		ptr := reflect.New(f.typ.Elem())
		if err := f.set(single, ptr.Elem()); err != nil {
			p.fail(errs, f, single, err.Error())
			return nil
		}
		fv.Set(ptr)
	default:
		if err := f.set(single, fv); err != nil {
			p.fail(errs, f, single, err.Error())
		}
	}
	return nil
}

func (p *Plan) fail(errs *[]FieldError, f *field, value, reason string) {
	*errs = append(*errs, FieldError{Source: f.source, Name: f.name, Value: value, Reason: reason})
}

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func bindJSON(r *http.Request, fv reflect.Value, o Options, errs *[]FieldError) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && !isJSONContentType(ct) {
		*errs = append(*errs, FieldError{Source: srcBody, Name: "body", Value: ct, Reason: "content type must be application/json"})
		return nil
	}
	// Read into a pooled buffer and unmarshal: fewer allocations than a
	// json.Decoder, and trailing data after the JSON value is rejected.
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= 64<<10 { // do not keep huge buffers alive
			bufPool.Put(buf)
		}
	}()
	if _, err := buf.ReadFrom(r.Body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return mbe
		}
		*errs = append(*errs, FieldError{Source: srcBody, Name: "body", Reason: err.Error()})
		return nil
	}
	if len(bytes.TrimSpace(buf.Bytes())) == 0 {
		return nil
	}
	var err error
	if o.StrictJSON {
		dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
		dec.DisallowUnknownFields()
		if err = dec.Decode(fv.Addr().Interface()); err == nil && dec.More() {
			err = errors.New("unexpected data after the JSON value")
		}
	} else {
		err = json.Unmarshal(buf.Bytes(), fv.Addr().Interface())
	}
	if err != nil {
		*errs = append(*errs, FieldError{Source: srcBody, Name: jsonFieldName(err), Reason: jsonReason(err)})
	}
	return nil
}

// isJSONContentType accepts application/json and any application/*+json,
// with or without parameters, without allocating.
func isJSONContentType(ct string) bool {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(ct)
	return strings.EqualFold(ct, "application/json") ||
		(len(ct) > len("application/") && strings.EqualFold(ct[:len("application/")], "application/") &&
			strings.HasSuffix(strings.ToLower(ct), "+json"))
}

func jsonFieldName(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Field != "" {
		return te.Field
	}
	return "body"
}

func jsonReason(err error) string {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	switch {
	case errors.As(err, &se):
		return fmt.Sprintf("invalid JSON at byte %d", se.Offset)
	case errors.As(err, &te):
		return fmt.Sprintf("must be %s, got %s", te.Type, te.Value)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "invalid JSON: unexpected end of input"
	}
	return err.Error()
}

func bindRaw(r *http.Request, fv reflect.Value, errs *[]FieldError) error {
	if r.Body == nil {
		return nil
	}
	if fv.Type() == readerType {
		fv.Set(reflect.ValueOf(r.Body))
		return nil
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return mbe
		}
		*errs = append(*errs, FieldError{Source: srcBody, Name: "body", Reason: err.Error()})
		return nil
	}
	if fv.Kind() == reflect.String {
		fv.SetString(string(b))
	} else {
		fv.SetBytes(b)
	}
	return nil
}
