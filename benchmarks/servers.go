package benchmarks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/dygoframework/dygo"
	"github.com/gin-gonic/gin"
	"github.com/go-chi/chi/v5"
)

// Servers returns every implementation, in the order they are reported.
func Servers() []Server {
	return []Server{
		{"net/http", NetHTTP()},
		{"chi", Chi()},
		{"gin", Gin()},
		{"huma", Huma()},
		{"dygo", Dygo()},
	}
}

// writeJSON is what a careful hand written handler does.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// handCreate is the JSON scenario for routers without binding (net/http, chi).
func handCreate(w http.ResponseWriter, r *http.Request, rawID string) {
	id, err := strconv.Atoi(rawID)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		if limit, err = strconv.Atoi(l); err != nil {
			http.Error(w, "bad limit", http.StatusBadRequest)
			return
		}
	}
	var body CreateUser
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, User{ID: id, Name: body.Name, Limit: limit})
}

func handGet(w http.ResponseWriter, rawID string) {
	id, err := strconv.Atoi(rawID)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, User{ID: id, Name: "ann"})
}

// NetHTTP is the baseline: Go 1.22+ ServeMux patterns, hand written parsing.
func NetHTTP() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") })
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) { handGet(w, r.PathValue("id")) })
	mux.HandleFunc("POST /users/{id}", func(w http.ResponseWriter, r *http.Request) { handCreate(w, r, r.PathValue("id")) })
	for i := range RouteCount {
		mux.HandleFunc("GET "+routePath(i)+"{id}", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, r.PathValue("id"))
		})
	}
	return mux
}

// Chi uses chi's router with the same hand written handlers.
func Chi() http.Handler {
	r := chi.NewRouter()
	r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "pong") })
	r.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) { handGet(w, chi.URLParam(r, "id")) })
	r.Post("/users/{id}", func(w http.ResponseWriter, r *http.Request) { handCreate(w, r, chi.URLParam(r, "id")) })
	for i := range RouteCount {
		r.Get(routePath(i)+"{id}", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, chi.URLParam(r, "id"))
		})
	}
	return r
}

// Gin uses gin's binding (ShouldBindUri, ShouldBindQuery, ShouldBindJSON).
func Gin() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New() // no default logger or recovery, like the others
	type uri struct {
		ID int `uri:"id" binding:"required"`
	}
	type query struct {
		Limit int `form:"limit,default=10"`
	}
	e.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	e.GET("/users/:id", func(c *gin.Context) {
		var u uri
		if err := c.ShouldBindUri(&u); err != nil {
			c.String(http.StatusBadRequest, "bad id")
			return
		}
		c.JSON(http.StatusOK, User{ID: u.ID, Name: "ann"})
	})
	e.POST("/users/:id", func(c *gin.Context) {
		var u uri
		var q query
		var body CreateUser
		if c.ShouldBindUri(&u) != nil || c.ShouldBindQuery(&q) != nil || c.ShouldBindJSON(&body) != nil {
			c.String(http.StatusBadRequest, "bad request")
			return
		}
		c.JSON(http.StatusCreated, User{ID: u.ID, Name: body.Name, Limit: q.Limit})
	})
	for i := range RouteCount {
		e.GET(routePath(i)+":id", func(c *gin.Context) { c.String(http.StatusOK, c.Param("id")) })
	}
	return e
}

type humaGetIn struct {
	ID int `path:"id"`
}

type humaCreateIn struct {
	ID    int        `path:"id"`
	Limit int        `query:"limit" default:"10"`
	Body  CreateUser `required:"true"`
}

type humaUserOut struct{ Body User }

type humaTextOut struct {
	ContentType string `header:"Content-Type"`
	Body        []byte
}

// Huma uses typed handlers on its net/http adapter, the closest design to
// Dygo. Its OpenAPI and docs routes are registered too, as in a real app.
func Huma() http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("bench", "1.0.0"))
	huma.Get(api, "/ping", func(context.Context, *struct{}) (*humaTextOut, error) {
		return &humaTextOut{ContentType: "text/plain", Body: []byte("pong")}, nil
	})
	huma.Get(api, "/users/{id}", func(_ context.Context, in *humaGetIn) (*humaUserOut, error) {
		return &humaUserOut{Body: User{ID: in.ID, Name: "ann"}}, nil
	})
	huma.Register(api, huma.Operation{Method: http.MethodPost, Path: "/users/{id}", DefaultStatus: http.StatusCreated},
		func(_ context.Context, in *humaCreateIn) (*humaUserOut, error) {
			return &humaUserOut{Body: User{ID: in.ID, Name: in.Body.Name, Limit: in.Limit}}, nil
		})
	for i := range RouteCount {
		huma.Get(api, routePath(i)+"{id}", func(_ context.Context, in *humaGetIn) (*humaTextOut, error) {
			return &humaTextOut{ContentType: "text/plain", Body: []byte(strconv.Itoa(in.ID))}, nil
		}, func(o *huma.Operation) { o.OperationID = "route-" + strconv.Itoa(i) })
	}
	return mux
}

type dygoGetIn struct {
	ID int `path:"id"`
}

type dygoCreateIn struct {
	ID    int        `path:"id"`
	Limit int        `query:"limit" default:"10"`
	Body  CreateUser `body:"json"`
}

type dygoController struct{}

func (dygoController) Routes(r dygo.Router) {
	r.Get("/ping", dygo.HandleNoInput(func(context.Context) (string, error) { return "pong", nil }))
	r.Get("/users/{id}", dygo.Handle(func(_ context.Context, in dygoGetIn) (User, error) {
		return User{ID: in.ID, Name: "ann"}, nil
	}))
	r.Post("/users/{id}", dygo.Handle(func(_ context.Context, in dygoCreateIn) (User, error) {
		return User{ID: in.ID, Name: in.Body.Name, Limit: in.Limit}, nil
	}))
	for i := range RouteCount {
		r.Get(routePath(i)+"{id}", dygo.Handle(func(_ context.Context, in dygoGetIn) (string, error) {
			return strconv.Itoa(in.ID), nil
		}))
	}
}

// Dygo uses typed handlers in a module, as an application would.
func Dygo() http.Handler {
	app, err := dygo.Create(context.Background(), &dygo.Module{
		Name:        "bench",
		Controllers: []dygo.Provider{dygo.Value(dygoController{})},
	})
	if err != nil {
		panic(err)
	}
	return app.Handler()
}
