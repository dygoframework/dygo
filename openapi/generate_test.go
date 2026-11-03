package openapi_test

import (
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/openapi"
)

// Paging is embedded in inputs without a source tag, so its fields count.
type Paging struct {
	Page int `query:"page"`
}

type searchIn struct {
	Paging
	Raw []byte `body:"raw"`
}

type formIn struct {
	Body struct {
		Name string `form:"name"`
	} `body:"form"`
}

type filesIn struct {
	Files []*multipart.FileHeader `file:"files"`
}

type Thing struct {
	Name string `json:"name"`
}

type moreCtrl struct{}

func (moreCtrl) Routes(r dygo.Router) {
	none := dygo.HandleNoInput(func(context.Context) (string, error) { return "", nil })
	r.Put("/things", none, openapi.Description("Replace things"), openapi.OperationID("replaceThings"))
	r.Patch("/things", none)
	r.Head("/things", none)
	r.Options("/things", none)
	r.All("/any", none)
	r.Post("/search", dygo.Handle(func(context.Context, searchIn) (string, error) { return "", nil }),
		openapi.ResponseOf[Thing](http.StatusConflict, "Already there"))
	r.Post("/form", dygo.Handle(func(context.Context, formIn) (string, error) { return "", nil }))
	r.Post("/files", dygo.Handle(func(context.Context, filesIn) (string, error) { return "", nil }))
	r.Version("2").Get("/v", none)
}

func generate(t *testing.T, o openapi.Options) map[string]any {
	t.Helper()
	app, err := dygo.Create(context.Background(), &dygo.Module{Name: "app", Controllers: []dygo.Provider{dygo.Value(moreCtrl{})}},
		dygo.WithVersioning(dygo.URIVersioning()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(openapi.Generate(app.Routes(), o))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestGenerateMethodsAndInputs(t *testing.T) {
	doc := generate(t, openapi.Options{Servers: []string{"https://api.example.com"}})
	b, _ := json.Marshal(doc)
	js := string(b)
	for _, want := range []string{
		`"servers":[{"url":"https://api.example.com"}]`,
		`"title":"API"`,
		`"operationId":"replaceThings"`,
		`"description":"Replace things"`,
		`"head":{`, `"options":{`, `"patch":{`,
		`"409":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Thing"}}},"description":"Already there"}`,
		`"application/octet-stream":{"schema":{"format":"binary","type":"string"}}`,
		`"name":"page"`,
		`"application/x-www-form-urlencoded"`,
		`"multipart/form-data":{"schema":{"properties":{"files":{"items":{"format":"binary","type":"string"},"type":"array"}}`,
		`_v2"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("document lacks %s", want)
		}
	}
	anyPath, _ := doc["paths"].(map[string]any)["/any"].(map[string]any)
	if len(anyPath) != 5 {
		t.Errorf("All route documented for %d methods, want 5", len(anyPath))
	}
}

func TestDocsUIWithoutPrefix(t *testing.T) {
	srv := dygotest.New(&dygo.Module{
		Name:    "app",
		Imports: []*dygo.Module{openapi.Module(openapi.Options{})},
	}).CompileHTTP(t)
	res := srv.GET("/docs").Do(t).Status(t, http.StatusOK)
	if !strings.Contains(res.Text(), `url: "/openapi.json"`) || !strings.Contains(res.Text(), "<title>API</title>") {
		t.Fatal(res.Text())
	}
}
