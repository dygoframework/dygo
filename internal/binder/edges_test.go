package binder

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCompileMoreErrors(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"query tag inside a form body", struct {
			Body struct {
				A string `query:"a"`
			} `body:"form"`
		}{}, "only form and file tags are allowed"},
		{"form body not a struct", struct {
			Body string `body:"form"`
		}{}, `body:"form" needs a struct`},
		{"bad field in a form body", struct {
			Body struct {
				A struct{ X int } `form:"a"`
			} `body:"form"`
		}{}, "cannot be bound"},
		{"raw body of the wrong type", struct {
			Body int `body:"raw"`
		}{}, `body:"raw" needs []byte, string or io.Reader`},
		{"unknown body kind", struct {
			Body string `body:"xml"`
		}{}, `unknown body kind "xml"`},
		{"file of the wrong type", struct {
			F string `file:"f"`
		}{}, "file tag needs *multipart.FileHeader"},
		{"slice path parameter", struct {
			IDs []int `path:"ids"`
		}{}, "path parameters cannot be slices"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(reflect.TypeOf(tt.v))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

func TestPlanInfo(t *testing.T) {
	type in struct {
		ID   int    `path:"id"`
		User string `user:""`
	}
	RegisterSource("user", func(*http.Request) (any, error) { return "u", nil })
	p, err := Compile(reflect.TypeFor[in]())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.PathNames(), []string{"id"}) || !p.UsesCustomSources() {
		t.Fatalf("PathNames %v, UsesCustomSources %v", p.PathNames(), p.UsesCustomSources())
	}
	type plain struct {
		Q string `query:"q"`
	}
	p, _ = Compile(reflect.TypeFor[plain]())
	if p.UsesCustomSources() {
		t.Fatal("plain input uses custom sources")
	}
}

func TestCustomSourceFailures(t *testing.T) {
	RegisterSource("failing", func(*http.Request) (any, error) { return nil, errors.New("no user") })
	RegisterSource("nothing", func(*http.Request) (any, error) { return nil, nil })
	RegisterSource("wrongtype", func(*http.Request) (any, error) { return 42, nil })
	type in struct {
		A string `failing:""`
		B string `nothing:""`
		C string `wrongtype:""`
	}
	_, err := bind[in](t, httptest.NewRequest("GET", "/", nil), Options{})
	var be *Error
	if !errors.As(err, &be) || len(be.Fields) != 2 {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "no user") || !strings.Contains(err.Error(), "source returned int") {
		t.Fatalf("got %v", err)
	}
}

func TestRequiredCookieAndFiles(t *testing.T) {
	type in struct {
		C     *http.Cookie            `cookie:"sid" required:"true"`
		F     *multipart.FileHeader   `file:"f" required:"true"`
		Files []*multipart.FileHeader `file:"many"`
	}
	_, err := bind[in](t, httptest.NewRequest("GET", "/", nil), Options{})
	var be *Error
	if !errors.As(err, &be) || len(be.Fields) != 2 {
		t.Fatalf("got %v", err)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, name := range []string{"f", "many", "many"} {
		fw, _ := mw.CreateFormFile(name, name+".txt")
		_, _ = fw.Write([]byte("x"))
	}
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(&http.Cookie{Name: "sid", Value: "1"})
	v, err := bind[in](t, r, Options{})
	if err != nil || v.C.Value != "1" || v.F == nil || len(v.Files) != 2 {
		t.Fatalf("%+v %v", v, err)
	}
}

// errReader fails every read.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestRawBody(t *testing.T) {
	type reader struct {
		Body io.Reader `body:"raw"`
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader("stream"))
	v, err := bind[reader](t, r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(v.Body); string(b) != "stream" {
		t.Fatalf("body %q", b)
	}

	type raw struct {
		Body []byte `body:"raw"`
	}
	r = httptest.NewRequest("POST", "/", nil)
	r.Body = nil
	if v, err := bind[raw](t, r, Options{}); err != nil || v.Body != nil {
		t.Fatalf("nil body: %v %v", v, err)
	}
	r = httptest.NewRequest("POST", "/", nil)
	r.Body = io.NopCloser(errReader{errors.New("broken")})
	if _, err := bind[raw](t, r, Options{}); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("broken body: %v", err)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 100)))
	r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 10)
	var mbe *http.MaxBytesError
	if _, err := bind[raw](t, r, Options{}); !errors.As(err, &mbe) {
		t.Fatalf("limit: %v", err)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader("bytes"))
	if v, err := bind[raw](t, r, Options{}); err != nil || string(v.Body) != "bytes" {
		t.Fatalf("bytes: %v %v", v, err)
	}
}

func TestJSONErrors(t *testing.T) {
	type in struct {
		Body struct {
			N int `json:"n"`
		} `body:"json"`
	}
	tests := []struct {
		name string
		body io.Reader
		want string
	}{
		{"cut off", strings.NewReader(`{"n":`), "invalid JSON"},
		{"read error", errReader{errors.New("conn reset")}, "conn reset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", nil)
			r.Body = io.NopCloser(tt.body)
			r.Header.Set("Content-Type", "application/json")
			_, err := bind[in](t, r, Options{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}
