package graphql_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/graphql"
	"github.com/dygoframework/dygo/graphql/internal/testgraph"
)

func benchGQL(b *testing.B, o graphql.Options) {
	res := &testgraph.Resolver{Users: map[string]*testgraph.User{"1": {ID: "1", Name: "Ada"}}}
	o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	o.Global = append(o.Global, dygo.Guards(adminGuard{}))
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
		b.Fatal(err)
	}
	h := app.Handler()
	body := `{"query":"query($id: ID!){ user(id: $id){ id name } me { id name } }","variables":{"id":"1"}}`
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			b.Fatal(w.Code, w.Body.String())
		}
	}
}

// The default options keep a query cache; QueryCacheSize -1 turns it off.
func BenchmarkGraphQL(b *testing.B)             { benchGQL(b, graphql.Options{}) }
func BenchmarkGraphQLNoQueryCache(b *testing.B) { benchGQL(b, graphql.Options{QueryCacheSize: -1}) }
