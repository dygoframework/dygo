package binder

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fuzz targets feed untrusted request data through Bind. Each one checks
// that Bind never panics, that it only returns nil, a *Error or an
// *http.MaxBytesError, and that every reported field names its source.

func checkBindErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var be *Error
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &be):
		if len(be.Fields) == 0 {
			t.Fatalf("*Error with no fields: %v", err)
		}
		for _, f := range be.Fields {
			if f.Source == "" || f.Name == "" || f.Reason == "" {
				t.Fatalf("incomplete field error %+v", f)
			}
		}
	case errors.As(err, &mbe):
	default:
		t.Fatalf("unexpected error type %T: %v", err, err)
	}
}

func mustCompile[T any](f *testing.F) *Plan {
	f.Helper()
	p, err := Compile(reflect.TypeFor[T]())
	if err != nil {
		f.Fatal(err)
	}
	return p
}

type fuzzScalars struct {
	ID      int64         `path:"id"`
	Slug    string        `path:"slug"`
	U8      uint8         `query:"u8"`
	I       int           `query:"i" default:"7"`
	F64     float64       `query:"f"`
	B       *bool         `query:"b"`
	Tags    []string      `query:"tag"`
	Ns      []int16       `query:"n"`
	Since   time.Time     `query:"since"`
	Wait    time.Duration `query:"wait"`
	IP      netip.Addr    `header:"X-Real-IP"`
	Tenant  string        `header:"X-Tenant" required:"true"`
	Session string        `cookie:"sid"`
	Raw     *http.Cookie  `cookie:"raw"`
}

// FuzzBindScalars covers path, query, header and cookie values of every
// scalar shape.
func FuzzBindScalars(f *testing.F) {
	p := mustCompile[fuzzScalars](f)
	f.Add("42", "post", "u8=7&i=-1&f=1.5&b=true&tag=a&tag=b&n=1&n=2&since=2026-10-29T10:00:00Z&wait=1m", "10.0.0.1", "acme", "s1")
	f.Add("x", "", "u8=300&f=NaN&b=maybe&n=99999&since=yesterday&wait=forever", "::1%eth0", "", "")
	f.Add("-9223372036854775809", "%zz", "%gh&&==&tag", "not-an-ip", "\x00", "\xff")
	f.Fuzz(func(t *testing.T, id, slug, rawQuery, ip, tenant, cookie string) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.RawQuery = rawQuery
		r.SetPathValue("id", id)
		r.SetPathValue("slug", slug)
		r.Header.Set("X-Real-IP", ip)
		if tenant != "" {
			r.Header.Set("X-Tenant", tenant)
		}
		r.Header.Set("Cookie", "sid="+cookie+"; raw="+cookie)
		var v fuzzScalars
		err := p.Bind(r, reflect.ValueOf(&v).Elem(), Options{})
		checkBindErr(t, err)
		if err != nil {
			return
		}
		// A successful bind must agree with strconv on the values it parsed.
		if n, perr := strconv.ParseInt(id, 10, 64); perr != nil || n != v.ID {
			t.Fatalf("path id %q bound as %d (strconv: %d, %v)", id, v.ID, n, perr)
		}
		if tenant == "" || v.Tenant != tenant {
			t.Fatalf("required header %q bound as %q", tenant, v.Tenant)
		}
		q, _ := url.ParseQuery(rawQuery)
		if !q.Has("i") && v.I != 7 {
			t.Fatalf("default not applied: %d", v.I)
		}
	})
}

type fuzzJSON struct {
	Body struct {
		Name  string            `json:"name"`
		Age   int               `json:"age"`
		Tags  []string          `json:"tags"`
		Attrs map[string]any    `json:"attrs"`
		Inner *struct{ X bool } `json:"inner"`
	} `body:"json"`
}

// FuzzBindJSON covers JSON bodies in lax and strict mode, with any content
// type and a small body limit.
func FuzzBindJSON(f *testing.F) {
	p := mustCompile[fuzzJSON](f)
	f.Add(`{"name":"a","age":1,"tags":["x"],"attrs":{"k":[1,{}]},"inner":{"X":true}}`, "application/json", false, int64(1<<20))
	f.Add(`{"name":1}`, "application/json; charset=utf-8", true, int64(1<<20))
	f.Add(`{"a":1} {"a":2}`, "application/vnd.api+json", true, int64(8))
	f.Add(`[`, "text/plain", false, int64(0))
	f.Add(``, "", false, int64(-1))
	f.Fuzz(func(t *testing.T, body, ct string, strict bool, limit int64) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		if limit >= 0 {
			r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, limit%(1<<16))
		}
		var v fuzzJSON
		checkBindErr(t, p.Bind(r, reflect.ValueOf(&v).Elem(), Options{StrictJSON: strict}))
	})
}

type fuzzForm struct {
	Title string                  `form:"title"`
	N     []int                   `form:"n"`
	File  *multipart.FileHeader   `file:"file"`
	Files []*multipart.FileHeader `file:"files"`
}

type fuzzFormBody struct {
	Body struct {
		Name string `form:"name"`
		Age  uint   `form:"age"`
	} `body:"form"`
}

// FuzzBindForm covers url-encoded and multipart forms, including malformed
// multipart boundaries.
func FuzzBindForm(f *testing.F) {
	files := mustCompile[fuzzForm](f)
	form := mustCompile[fuzzFormBody](f)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", "cv")
	_ = mw.WriteField("n", "3")
	fw, _ := mw.CreateFormFile("file", "cv.pdf")
	_, _ = fw.Write([]byte("%PDF"))
	_ = mw.Close()
	f.Add(buf.Bytes(), mw.FormDataContentType())
	f.Add([]byte("name=a&age=3&title=t&n=1&n=x"), "application/x-www-form-urlencoded")
	f.Add([]byte("--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"\"\r\n\r\n"), "multipart/form-data; boundary=b")
	f.Add([]byte("%%%"), "multipart/form-data")
	f.Fuzz(func(t *testing.T, body []byte, ct string) {
		for _, p := range []*Plan{files, form} {
			r := httptest.NewRequest(http.MethodPost, "/?n=9", bytes.NewReader(body))
			r.Header.Set("Content-Type", ct)
			r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 1<<16)
			dst := reflect.New(p.typ).Elem()
			checkBindErr(t, p.Bind(r, dst, Options{}))
		}
	})
}
