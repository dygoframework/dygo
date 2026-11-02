package binder

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

type Page struct {
	Page  int `query:"page"  default:"1"`
	Limit int `query:"limit" default:"20"`
}

type input struct {
	Page
	ID      int64         `path:"id"`
	Tags    []string      `query:"tag"`
	Active  *bool         `query:"active"`
	Since   time.Time     `query:"since"`
	Wait    time.Duration `query:"wait"`
	IP      netip.Addr    `header:"X-Real-IP"`
	Tenant  string        `header:"X-Tenant" required:"true"`
	Session string        `cookie:"sid"`
	Body    struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	} `body:"json"`
	Note string // untagged fields are left alone
}

func bind[T any](t *testing.T, r *http.Request, o Options) (T, error) {
	t.Helper()
	var v T
	p, err := Compile(reflect.TypeFor[T]())
	if err != nil {
		t.Fatal(err)
	}
	return v, p.Bind(r, reflect.ValueOf(&v).Elem(), o)
}

func req(method, target, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func TestBindAllSources(t *testing.T) {
	r := req("POST", "/users/42?tag=a&tag=b&active=true&since=2026-10-29T10:00:00Z&wait=1m&limit=5",
		`{"name":"Dylan","age":30}`)
	r.SetPathValue("id", "42")
	r.Header.Set("X-Real-IP", "10.0.0.1")
	r.Header.Set("X-Tenant", "acme")
	r.AddCookie(&http.Cookie{Name: "sid", Value: "s1"})

	in, err := bind[input](t, r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if in.ID != 42 || strings.Join(in.Tags, ",") != "a,b" || in.Active == nil || !*in.Active ||
		in.Since.Year() != 2026 || in.Wait != time.Minute || in.IP.String() != "10.0.0.1" ||
		in.Tenant != "acme" || in.Session != "s1" || in.Body.Name != "Dylan" || in.Body.Age != 30 ||
		in.Page.Page != 1 || in.Limit != 5 {
		t.Fatalf("%+v", in)
	}
}

func TestBindCollectsAllErrors(t *testing.T) {
	r := req("POST", "/users/x?active=maybe&limit=-", `{"name":1}`)
	r.SetPathValue("id", "x")
	_, err := bind[input](t, r, Options{})
	var be *Error
	if !errors.As(err, &be) {
		t.Fatalf("got %v", err)
	}
	got := map[string]string{}
	for _, f := range be.Fields {
		got[f.Source+"."+f.Name] = f.Reason
	}
	for _, k := range []string{"path.id", "query.active", "query.limit", "header.X-Tenant", "body.name"} {
		if got[k] == "" {
			t.Errorf("missing error for %s in %v", k, got)
		}
	}
}

func TestStrictJSONAndContentType(t *testing.T) {
	type in struct {
		Body struct{ A int } `body:"json"`
	}
	if _, err := bind[in](t, req("POST", "/", `{"A":1,"B":2}`), Options{StrictJSON: true}); err == nil {
		t.Fatal("strict JSON accepted an unknown field")
	}
	r := req("POST", "/", `{"A":1}`)
	r.Header.Set("Content-Type", "text/plain")
	if _, err := bind[in](t, r, Options{}); err == nil || !strings.Contains(err.Error(), "content type") {
		t.Fatalf("got %v", err)
	}
	if v, err := bind[in](t, httptest.NewRequest("POST", "/", nil), Options{}); err != nil || v.Body.A != 0 {
		t.Fatal("empty body should leave the field zero")
	}
}

func TestTrailingJSONAndContentTypes(t *testing.T) {
	type in struct {
		Body struct{ A int } `body:"json"`
	}
	for _, strict := range []bool{false, true} {
		if _, err := bind[in](t, req("POST", "/", `{"A":1} {"A":2}`), Options{StrictJSON: strict}); err == nil {
			t.Errorf("strict=%v: trailing data accepted", strict)
		}
	}
	for ct, want := range map[string]bool{
		"application/json": true, "application/json; charset=utf-8": true, "Application/JSON": true,
		"application/vnd.api+json": true, "text/plain": false, "application/xml": false, "json": false,
	} {
		if got := isJSONContentType(ct); got != want {
			t.Errorf("isJSONContentType(%q) = %v", ct, got)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	type in struct {
		Body map[string]string `body:"json"`
	}
	r := req("POST", "/", `{"a":"`+strings.Repeat("x", 100)+`"}`)
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 10)
	_, err := bind[in](t, r, Options{})
	var mbe *http.MaxBytesError
	if !errors.As(err, &mbe) {
		t.Fatalf("got %v", err)
	}
}

func TestMultipartAndRaw(t *testing.T) {
	type upload struct {
		Title string                `form:"title"`
		File  *multipart.FileHeader `file:"file"`
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", "cv")
	fw, _ := mw.CreateFormFile("file", "cv.pdf")
	_, _ = fw.Write([]byte("%PDF"))
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	u, err := bind[upload](t, r, Options{})
	if err != nil || u.Title != "cv" || u.File == nil || u.File.Filename != "cv.pdf" {
		t.Fatalf("%+v %v", u, err)
	}

	type raw struct {
		Body string `body:"raw"`
	}
	v, err := bind[raw](t, httptest.NewRequest("POST", "/", strings.NewReader("hello")), Options{})
	if err != nil || v.Body != "hello" {
		t.Fatal(v, err)
	}

	type form struct {
		Body struct {
			Name string `form:"name"`
			N    int    `form:"n"`
		} `body:"form"`
	}
	fr := httptest.NewRequest("POST", "/", strings.NewReader("name=a&n=3"))
	fr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f, err := bind[form](t, fr, Options{})
	if err != nil || f.Body.Name != "a" || f.Body.N != 3 {
		t.Fatal(f, err)
	}
}

type userID string

func TestCustomSourceAndConverter(t *testing.T) {
	RegisterSource("user", func(r *http.Request) (any, error) { return userID(r.Header.Get("X-User")), nil })
	type money int64
	RegisterConverter(reflect.TypeFor[money](), func(s string) (any, error) {
		s = strings.TrimPrefix(s, "$")
		var m int64
		for _, c := range s {
			if c < '0' || c > '9' {
				return nil, errors.New("must look like $123")
			}
			m = m*10 + int64(c-'0')
		}
		return money(m), nil
	})
	type in struct {
		User  userID `user:""`
		Price money  `query:"price"`
	}
	r := httptest.NewRequest("GET", "/?price=$15", nil)
	r.Header.Set("X-User", "u1")
	v, err := bind[in](t, r, Options{})
	if err != nil || v.User != "u1" || v.Price != 15 {
		t.Fatal(v, err)
	}
}

func TestCompileErrors(t *testing.T) {
	cases := map[string]any{
		"more than one source tag": struct {
			A string `query:"a" header:"a"`
		}{},
		"must be exported": struct {
			a string `query:"a"`
		}{},
		"cannot be bound": struct {
			A struct{ X int } `query:"a"`
		}{},
		"needs a name": struct {
			A string `query:""`
		}{},
		"only one is allowed": struct {
			A struct{} `body:"json"`
			B []byte   `body:"raw"`
		}{},
		"default \"x\"": struct {
			A int `query:"a" default:"x"`
		}{},
		"must be a struct": 3,
	}
	for want, v := range cases {
		_, err := Compile(reflect.TypeOf(v))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

func FuzzQueryInt(f *testing.F) {
	type in struct {
		N  int8    `query:"n"`
		F  float32 `query:"f"`
		Bs []bool  `query:"b"`
	}
	p, err := Compile(reflect.TypeFor[in]())
	if err != nil {
		f.Fatal(err)
	}
	f.Add("1", "2.5", "true")
	f.Add("999", "x", "")
	f.Fuzz(func(_ *testing.T, n, fl, b string) {
		r := httptest.NewRequest("GET", "/", nil)
		q := r.URL.Query()
		q.Set("n", n)
		q.Set("f", fl)
		q.Add("b", b)
		r.URL.RawQuery = q.Encode()
		var v in
		_ = p.Bind(r, reflect.ValueOf(&v).Elem(), Options{})
	})
}
