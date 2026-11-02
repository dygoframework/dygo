package openapi_test

import (
	"context"
	"encoding/json"
	"mime/multipart"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/openapi"
)

type Page[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

type User struct {
	ID        string    `json:"id" example:"u1"`
	Email     string    `json:"email" validate:"required,email"`
	Nick      *string   `json:"nick,omitempty"`
	Friends   []*User   `json:"friends,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	secret    string    //nolint:unused // checks unexported fields are skipped
}

type ListIn struct {
	Limit  int      `query:"limit" default:"20" validate:"gte=1,lte=100" description:"page size"`
	Sort   string   `query:"sort" validate:"oneof=name date"`
	IDs    []string `query:"id"`
	Tenant string   `header:"X-Tenant" required:"true"`
}

type CreateIn struct {
	Body struct {
		Email string `json:"email" validate:"required,email"`
		Name  string `json:"name" validate:"min=2,max=50"`
	} `body:"json"`
}

type UploadIn struct {
	Title string                `form:"title" required:"true"`
	File  *multipart.FileHeader `file:"file"`
}

type ctrl struct{}

func (ctrl) Routes(r dygo.Router) {
	r = r.Prefix("/users").Use(openapi.Tags("users"))
	r.Get("/", dygo.Handle(func(context.Context, ListIn) (Page[User], error) { return Page[User]{}, nil }),
		openapi.Summary("List users"))
	r.Get("/{id}", dygo.Handle(func(context.Context, struct {
		ID string `path:"id"`
	},
	) (*User, error) {
		return nil, nil
	}), openapi.Response(404, "No such user"), openapi.Tags("lookup"))
	r.Post("/", dygo.Handle(func(context.Context, CreateIn) (User, error) { return User{}, nil }), openapi.Public())
	r.Delete("/{id}", dygo.Handle(func(context.Context, struct {
		ID string `path:"id"`
	},
	) (dygo.NoContent, error) {
		return dygo.NoContent{}, nil
	}), openapi.Deprecated())
	r.Post("/avatar", dygo.Handle(func(context.Context, UploadIn) (string, error) { return "", nil }), dygo.HTTPCode(200))
	r.Get("/files/{path...}", dygo.HandleNoInput(func(context.Context) ([]byte, error) { return nil, nil }))
	r.Get("/hidden", dygo.HandleNoInput(func(context.Context) (string, error) { return "", nil }), openapi.Exclude())
}

func TestDocument(t *testing.T) {
	srv := dygotest.New(&dygo.Module{
		Name:        "app",
		Imports:     []*dygo.Module{openapi.Module(openapi.Options{Info: openapi.Info{Title: "Users"}, BearerAuth: true})},
		Controllers: []dygo.Provider{dygo.Value(ctrl{})},
	}).With(dygo.WithGlobalPrefix("/api")).CompileHTTP(t)

	var doc map[string]any
	srv.GET("/api/openapi.json").Do(t).Status(t, 200).JSON(t, &doc)
	get := func(path ...string) any {
		var v any = doc
		for _, p := range path {
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("%v: not an object at %q", path, p)
			}
			v = m[p]
		}
		return v
	}
	// Re-encoding the decoded map sorts the keys, so expectations use sorted keys.
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	if get("info", "title") != "Users" || get("openapi") != "3.1.0" {
		t.Fatal(js(doc["info"]))
	}
	paths := get("paths").(map[string]any)
	for _, p := range []string{"/api/users", "/api/users/{id}", "/api/users/avatar", "/api/users/files/{path}"} {
		if paths[p] == nil {
			t.Errorf("missing path %s in %v", p, js(paths))
		}
	}
	if paths["/api/users/hidden"] != nil || paths["/api/openapi.json"] != nil {
		t.Error("excluded routes listed")
	}

	list := get("paths", "/api/users", "get").(map[string]any)
	if list["summary"] != "List users" || js(list["tags"]) != `["users"]` {
		t.Error(js(list))
	}
	params := js(list["parameters"])
	for _, want := range []string{
		`{"description":"page size","in":"query","name":"limit","schema":{"default":20,"format":"int64","maximum":100,"minimum":1,"type":"integer"}}`,
		`"enum":["name","date"]`,
		`{"explode":true,"in":"query","name":"id","schema":{"items":{"type":"string"},"type":"array"}}`,
		`{"in":"header","name":"X-Tenant","required":true`,
	} {
		if !strings.Contains(params, want) {
			t.Errorf("parameters lack %s\n%s", want, params)
		}
	}
	if ref := js(get("paths", "/api/users", "get", "responses", "200", "content", "application/json", "schema")); ref != `{"$ref":"#/components/schemas/Page_User"}` {
		t.Error(ref)
	}

	byID := js(get("paths", "/api/users/{id}", "get"))
	for _, want := range []string{`"description":"No such user"}`, `"tags":["users","lookup"]`, `"in":"path","name":"id","required":true`} {
		if !strings.Contains(byID, want) {
			t.Errorf("get by id lacks %s\n%s", want, byID)
		}
	}

	create := js(get("paths", "/api/users", "post"))
	for _, want := range []string{`"201"`, `"security":[]`, `"minLength":2`, `"format":"email"`, `"required":["email","name"]`} {
		if !strings.Contains(create, want) {
			t.Errorf("create lacks %s\n%s", want, create)
		}
	}
	if del := js(get("paths", "/api/users/{id}", "delete")); !strings.Contains(del, `"deprecated":true`) || !strings.Contains(del, `"204"`) {
		t.Error(del)
	}
	if up := js(get("paths", "/api/users/avatar", "post")); !strings.Contains(up, `"multipart/form-data"`) ||
		!strings.Contains(up, `"file":{"format":"binary","type":"string"}`) || !strings.Contains(up, `"200":{"content":{"text/plain"`) {
		t.Error(up)
	}

	user := js(get("components", "schemas", "User"))
	for _, want := range []string{
		`"createdAt":{"format":"date-time","type":"string"}`, `"friends":{"items":{"$ref":"#/components/schemas/User"},"type":"array"}`,
		`"examples":["u1"]`, `"required":["id","email","createdAt"]`,
	} {
		if !strings.Contains(user, want) {
			t.Errorf("User schema lacks %s\n%s", want, user)
		}
	}
	if strings.Contains(user, "secret") || strings.Contains(user, `"nick"],`) {
		t.Error(user)
	}
	if js(get("security")) != `[{"bearer":[]}]` {
		t.Error(js(get("security")))
	}

	html := srv.GET("/api/docs").Do(t).Status(t, 200).Text()
	if !strings.Contains(html, `url: "/api/openapi.json"`) || !strings.Contains(html, "swagger-ui-bundle.js") {
		t.Fatal(html)
	}
}
