package graphql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/graphql"
	"github.com/dygoframework/dygo/graphql/internal/testgraph"
)

type adminKey struct{}

// adminGuard requires X-Admin on fields marked with adminKey.
type adminGuard struct{}

func (adminGuard) CanActivate(ec dygo.ExecutionContext) (bool, error) {
	if _, ok := ec.RouteMetadata(adminKey{}); !ok {
		return true, nil
	}
	if ec.GraphQL().Request().Header.Get("X-Admin") != "yes" {
		return false, errs.Unauthorized("admin only")
	}
	return true, nil
}

type seen struct {
	mu     sync.Mutex
	fields []string
}

func setup(t *testing.T, o graphql.Options) (*httptest.Server, *testgraph.Resolver, *seen) {
	t.Helper()
	res := &testgraph.Resolver{Users: map[string]*testgraph.User{"1": {ID: "1", Name: "Ada"}}}
	s := &seen{}
	o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	o.Global = append(o.Global,
		dygo.Guards(adminGuard{}),
		dygo.Interceptors(dygo.InterceptorFunc(func(ec dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
			if ec.Kind() != dygo.KindGraphQL || ec.HTTP() != nil {
				t.Errorf("kind %q, HTTP host %v", ec.Kind(), ec.HTTP())
			}
			g := ec.GraphQL()
			s.mu.Lock()
			s.fields = append(s.fields, g.Field()+" "+jsonOf(g.Args()))
			s.mu.Unlock()
			return next.Handle()
		})),
	)
	app, err := dygo.Create(context.Background(), &dygo.Module{
		Name: "app",
		Providers: []dygo.Provider{
			dygo.Value(res),
			dygo.Provide(func(r *testgraph.Resolver) graphql.ExecutableSchema {
				return testgraph.NewExecutableSchema(testgraph.Config{Resolvers: r})
			}),
		},
		Controllers: []dygo.Provider{graphql.Endpoint(o)},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	return srv, res, s
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

type response struct {
	Data   map[string]any `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Path       []any          `json:"path"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

func post(t *testing.T, srv *httptest.Server, query string, vars map[string]any, header http.Header) response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestQuery(t *testing.T) {
	srv, _, s := setup(t, graphql.Options{})
	r := post(t, srv, `query($id: ID!) { user(id: $id) { id name } me { name } }`, map[string]any{"id": "1"}, nil)
	if len(r.Errors) > 0 {
		t.Fatalf("errors: %+v", r.Errors)
	}
	if got := jsonOf(r.Data); got != `{"me":{"name":"Ada"},"user":{"id":"1","name":"Ada"}}` {
		t.Errorf("data = %s", got)
	}
	if got := strings.Join(s.fields, "; "); !strings.Contains(got, `Query.user {"id":"1"}`) || !strings.Contains(got, "Query.me {}") {
		t.Errorf("fields seen = %s", got)
	}
}

func TestGET(t *testing.T) {
	srv, _, _ := setup(t, graphql.Options{})
	resp, err := http.Get(srv.URL + "/graphql?query=" + "%7Bme%7Bid%7D%7D")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != `{"data":{"me":{"id":"1"}}}` {
		t.Errorf("GET = %s", b)
	}
}

func TestFieldGuard(t *testing.T) {
	srv, res, _ := setup(t, graphql.Options{
		Fields: map[string][]dygo.RouteOption{"Mutation.deleteUser": {dygo.SetMetadata(adminKey{}, true)}},
	})
	r := post(t, srv, `mutation { deleteUser(id: "1") }`, nil, nil)
	if len(r.Errors) != 1 || r.Errors[0].Message != "admin only" || r.Errors[0].Extensions["code"] != "UNAUTHENTICATED" || r.Errors[0].Extensions["statusCode"] != 401.0 {
		t.Fatalf("errors = %+v", r.Errors)
	}
	if len(res.Deleted) != 0 {
		t.Fatalf("resolver ran: %v", res.Deleted)
	}
	r = post(t, srv, `mutation { deleteUser(id: "1") }`, nil, http.Header{"X-Admin": {"yes"}})
	if len(r.Errors) > 0 || r.Data["deleteUser"] != true || len(res.Deleted) != 1 {
		t.Fatalf("with header: %+v, deleted %v", r, res.Deleted)
	}
	// Queries are not marked, so the guard lets them through.
	if r := post(t, srv, `{ me { id } }`, nil, nil); len(r.Errors) > 0 {
		t.Fatalf("query: %+v", r.Errors)
	}
}

func TestErrors(t *testing.T) {
	srv, _, _ := setup(t, graphql.Options{})
	for _, tc := range []struct{ id, msg, code string }{
		{"9", "user not found", "NOT_FOUND"},
		{"broken", "Internal server error", "INTERNAL_SERVER_ERROR"},
		{"panic", "Internal server error", "INTERNAL_SERVER_ERROR"},
	} {
		r := post(t, srv, `query($id: ID!) { user(id: $id) { id } }`, map[string]any{"id": tc.id}, nil)
		if len(r.Errors) != 1 || r.Errors[0].Message != tc.msg || r.Errors[0].Extensions["code"] != tc.code {
			t.Errorf("user(%s): errors = %+v", tc.id, r.Errors)
			continue
		}
		if jsonOf(r.Errors[0].Path) != `["user"]` {
			t.Errorf("user(%s): path = %v", tc.id, r.Errors[0].Path)
		}
	}
	// gqlgen's own validation errors pass through.
	r := post(t, srv, `{ nope }`, nil, nil)
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0].Message, "nope") {
		t.Errorf("validation: %+v", r.Errors)
	}
}

func TestUnknownFieldFailsBoot(t *testing.T) {
	_, err := dygo.Create(context.Background(), &dygo.Module{
		Name: "app",
		Providers: []dygo.Provider{
			dygo.Provide(func() graphql.ExecutableSchema {
				return testgraph.NewExecutableSchema(testgraph.Config{Resolvers: &testgraph.Resolver{}})
			}),
		},
		Controllers: []dygo.Provider{graphql.Endpoint(graphql.Options{
			Fields: map[string][]dygo.RouteOption{"Query.users": nil},
		})},
	})
	if err == nil || !strings.Contains(err.Error(), `no root field "Query.users"`) || !strings.Contains(err.Error(), "Query.user)") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlayground(t *testing.T) {
	srv, _, _ := setup(t, graphql.Options{Playground: true})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/graphql", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "<html") {
		t.Errorf("playground body = %.200s", b)
	}
}

func TestCode(t *testing.T) {
	for status, want := range map[int]string{400: "BAD_USER_INPUT", 422: "BAD_USER_INPUT", 403: "FORBIDDEN", 409: "CONFLICT", 418: "I'M_A_TEAPOT", 503: "INTERNAL_SERVER_ERROR"} {
		if got := graphql.Code(status); got != want {
			t.Errorf("Code(%d) = %q, want %q", status, got, want)
		}
	}
}
