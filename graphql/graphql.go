// Package graphql serves a gqlgen schema in a Dygo app, schema first. gqlgen
// generates the executable schema and resolver interfaces from .graphqls
// files; the root resolver is an ordinary provider, so resolvers get their
// services through dependency injection:
//
//	func NewResolver(users *users.Service) *graph.Resolver { return &graph.Resolver{Users: users} }
//
//	func NewSchema(r *graph.Resolver) graphql.ExecutableSchema {
//		return graph.NewExecutableSchema(graph.Config{Resolvers: r})
//	}
//
//	var Module = &dygo.Module{
//		Name:        "graph",
//		Imports:     []*dygo.Module{users.Module},
//		Providers:   []dygo.Provider{dygo.Provide(NewResolver), dygo.Provide(NewSchema)},
//		Controllers: []dygo.Provider{graphql.Endpoint(graphql.Options{
//			Global: []dygo.RouteOption{dygo.Guards(dygo.Ref[*auth.Guard]())},
//			Fields: map[string][]dygo.RouteOption{"Mutation.deleteUser": {auth.Roles("admin")}},
//		})},
//	}
//
// Guards, interceptors and filters run around every root field (each query,
// mutation and subscription field) with
// ExecutionContext.Kind() == dygo.KindGraphQL and the field in
// ExecutionContext.GraphQL(). Errors are GraphQL errors with an
// extensions.code (NOT_FOUND, UNAUTHENTICATED, FORBIDDEN, BAD_USER_INPUT,
// INTERNAL_SERVER_ERROR) and extensions.statusCode from an *errs.HTTPError;
// other errors and panics are hidden behind "Internal server error".
package graphql

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	gql "github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// ExecutableSchema is gqlgen's generated schema. Provide one in the module
// that holds the [Endpoint].
type ExecutableSchema = gql.ExecutableSchema

// Options configures [Endpoint].
type Options struct {
	// Path of the endpoint, "/graphql" by default.
	Path string
	// Playground serves GraphiQL at Path with GET when the request accepts
	// text/html. Off by default; turn it on in development.
	Playground bool
	// Global enhancers for every root field.
	Global []dygo.RouteOption
	// InheritAppConfig also runs the app's global enhancers. GraphQL rides on
	// HTTP, but the hosts differ: HTTP() is nil inside a field.
	InheritAppConfig bool
	// Fields adds enhancers and metadata to root fields, keyed "Type.field"
	// ("Query.user", "Mutation.deleteUser"). Unknown fields fail at boot.
	Fields map[string][]dygo.RouteOption
	// Introspection is on unless this is set.
	NoIntrospection bool
	// Transports replaces the default ones (POST, GET, multipart form); add
	// transport.Websocket here for subscriptions.
	Transports []gql.Transport
	// QueryCacheSize is how many parsed and validated queries are kept, 1000
	// by default, as in gqlgen's NewDefaultServer. Negative turns the cache
	// off. Automatic persisted queries are on with a cache a tenth that size.
	QueryCacheSize int
	// Configure can change the gqlgen server further (complexity limits,
	// caches, extensions).
	Configure func(s *handler.Server)
	// Logger gets internal errors and panics; slog.Default() when nil.
	Logger *slog.Logger
}

// Endpoint returns the controller that serves the module's ExecutableSchema.
func Endpoint(o Options) dygo.Provider {
	if o.Path == "" {
		o.Path = "/graphql"
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return dygo.Provide(func(es ExecutableSchema, ref *dygo.ModuleRef) *Controller {
		return &Controller{o: o, es: es, ref: ref}
	})
}

// Controller serves a schema; [Endpoint] provides it.
type Controller struct {
	o   Options
	es  ExecutableSchema
	ref *dygo.ModuleRef

	srv       *handler.Server
	pipelines map[string]*dygo.Pipeline
	fallback  *dygo.Pipeline
	page      http.Handler
}

// Routes implements dygo.Controller.
func (c *Controller) Routes(r dygo.Router) {
	serve := dygo.HandleRaw(func(w http.ResponseWriter, req *http.Request) error {
		if c.page != nil && req.Method == http.MethodGet && req.URL.RawQuery == "" && strings.Contains(req.Header.Get("Accept"), "text/html") {
			c.page.ServeHTTP(w, req)
			return nil
		}
		c.srv.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), requestKey{}, req)))
		return nil
	})
	r.Post(c.o.Path, serve)
	r.Get(c.o.Path, serve)
}

type requestKey struct{}

// OnModuleInit builds the gqlgen server and the field pipelines.
func (c *Controller) OnModuleInit(context.Context) error {
	app := c.ref.App()
	enh, err := dygo.NewEnhancers(app, c.o.Global, c.o.InheritAppConfig)
	if err != nil {
		return err
	}
	known := rootFields(c.es)
	var problems []error
	c.pipelines = map[string]*dygo.Pipeline{}
	for field, opts := range c.o.Fields {
		if !slices.Contains(known, field) {
			problems = append(problems, fmt.Errorf("graphql: Fields: the schema has no root field %q (it has %s)", field, strings.Join(known, ", ")))
			continue
		}
		pl, err := enh.Pipeline(dygo.Endpoint{Kind: dygo.KindGraphQL, Pattern: field, Owner: c, Handler: field, Options: opts})
		if err != nil {
			problems = append(problems, err)
			continue
		}
		c.pipelines[field] = pl
	}
	if c.fallback, err = enh.Pipeline(dygo.Endpoint{Kind: dygo.KindGraphQL, Pattern: "*", Owner: c, Handler: "graphql"}); err != nil {
		problems = append(problems, err)
	}
	if err := errors.Join(problems...); err != nil {
		return err
	}

	srv := handler.New(c.es)
	ts := c.o.Transports
	if ts == nil {
		ts = []gql.Transport{transport.Options{}, transport.GET{}, transport.POST{}, transport.MultipartForm{}}
	}
	for _, t := range ts {
		srv.AddTransport(t)
	}
	if !c.o.NoIntrospection {
		srv.Use(extension.Introspection{})
	}
	if n := c.o.QueryCacheSize; n >= 0 {
		if n == 0 {
			n = 1000
		}
		srv.SetQueryCache(lru.New[*ast.QueryDocument](n))
		srv.Use(extension.AutomaticPersistedQuery{Cache: lru.New[string](max(1, n/10))})
	}
	srv.SetErrorPresenter(c.presentError)
	srv.SetRecoverFunc(func(ctx context.Context, v any) error {
		return &dygo.PanicError{Value: v}
	})
	srv.AroundRootFields(c.aroundRootField)
	if c.o.Configure != nil {
		c.o.Configure(srv)
	}
	c.srv = srv
	if c.o.Playground {
		c.page = playground.Handler("GraphQL", c.o.Path)
	}
	return nil
}

// rootFields lists "Type.field" for the query, mutation and subscription
// types.
func rootFields(es ExecutableSchema) []string {
	s := es.Schema()
	var out []string
	for _, def := range []*ast.Definition{s.Query, s.Mutation, s.Subscription} {
		if def == nil {
			continue
		}
		for _, f := range def.Fields {
			if !strings.HasPrefix(f.Name, "__") {
				out = append(out, def.Name+"."+f.Name)
			}
		}
	}
	slices.Sort(out)
	return out
}

// field is the GraphQLHost of one root field.
type field struct {
	name string
	args map[string]any
	req  *http.Request
}

func (f *field) Field() string          { return f.name }
func (f *field) Args() map[string]any   { return f.args }
func (f *field) Request() *http.Request { return f.req }

func (c *Controller) aroundRootField(ctx context.Context, next gql.RootResolver) gql.Marshaler {
	rf := gql.GetRootFieldContext(ctx)
	if rf == nil {
		return next(ctx)
	}
	name := rf.Field.Name
	if od := rf.Field.ObjectDefinition; od != nil {
		name = od.Name + "." + name
	}
	pl := c.pipelines[name]
	if pl == nil {
		pl = c.fallback
	}
	f := &field{name: name, args: rf.Field.ArgumentMap(gql.GetOperationContext(ctx).Variables)}
	f.req, _ = ctx.Value(requestKey{}).(*http.Request)
	out, err := pl.Run(ctx, f, nil, func(ctx context.Context) (any, error) { return next(ctx), nil })
	if err != nil {
		if !errors.Is(err, dygo.ErrFiltered) {
			gql.AddError(ctx, err)
		}
		return gql.Null
	}
	if m, ok := out.(gql.Marshaler); ok {
		return m
	}
	return gql.Null
}

// presentError maps errors to GraphQL errors with an extensions.code.
func (c *Controller) presentError(ctx context.Context, err error) *gqlerror.Error {
	ge := gql.DefaultErrorPresenter(ctx, err)
	var pe *dygo.PanicError
	if errors.As(err, &pe) {
		c.o.Logger.ErrorContext(ctx, "graphql: resolver panicked", "path", ge.Path.String(), "panic", pe.Value)
		return internal(ge)
	}
	var already *gqlerror.Error
	if errors.As(err, &already) && already.Err == nil {
		return ge // a parse or validation error from gqlgen
	}
	cause := err
	if already != nil && already.Err != nil {
		cause = already.Err
	}
	var he *errs.HTTPError
	var conv errs.Converter
	if !errors.As(cause, &he) && !errors.As(cause, &conv) {
		c.o.Logger.ErrorContext(ctx, "graphql: resolver failed", "path", ge.Path.String(), "err", cause)
		return internal(ge)
	}
	b := errs.ToBody(cause)
	if b.StatusCode >= 500 {
		return internal(ge)
	}
	ge.Message = b.Message
	ge.Extensions = map[string]any{"code": Code(b.StatusCode), "statusCode": b.StatusCode}
	if b.Code != "" {
		ge.Extensions["reason"] = b.Code
	}
	if b.Details != nil {
		ge.Extensions["details"] = b.Details
	}
	return ge
}

func internal(ge *gqlerror.Error) *gqlerror.Error {
	ge.Message = "Internal server error"
	ge.Extensions = map[string]any{"code": "INTERNAL_SERVER_ERROR", "statusCode": 500}
	return ge
}

// Code is the Apollo-style error code for an HTTP status.
func Code(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "BAD_USER_INPUT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusTooManyRequests:
		return "TOO_MANY_REQUESTS"
	}
	if status >= 500 {
		return "INTERNAL_SERVER_ERROR"
	}
	return strings.ToUpper(strings.ReplaceAll(http.StatusText(status), " ", "_"))
}
