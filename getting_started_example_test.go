package dygo_test

// This file is the code of docs/getting-started.md, so the page's examples are
// compiled and tested. Names carry a gs prefix to stay apart from other tests.

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/validate"
)

type gsProduct struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Price int    `json:"price"`
}

type gsService struct {
	mu    sync.Mutex
	items map[int]gsProduct
	next  int
}

func newGSService() *gsService { return &gsService{items: map[int]gsProduct{}} }

func (s *gsService) Find(id int) (gsProduct, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[id]
	return p, ok
}

func (s *gsService) Add(name string, price int) gsProduct {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	p := gsProduct{ID: s.next, Name: name, Price: price}
	s.items[p.ID] = p
	return p
}

type gsController struct{ svc *gsService }

func newGSController(svc *gsService) *gsController { return &gsController{svc: svc} }

func (c *gsController) Routes(r dygo.Router) {
	r = r.Prefix("/products")
	r.Get("/{id}", dygo.Handle(c.Get))
	r.Post("/", dygo.Handle(c.Create))
}

type gsGetIn struct {
	ID int `path:"id"`
}

func (c *gsController) Get(_ context.Context, in gsGetIn) (gsProduct, error) {
	p, ok := c.svc.Find(in.ID)
	if !ok {
		return gsProduct{}, errs.NotFound("product not found")
	}
	return p, nil
}

type gsCreateIn struct {
	Body struct {
		Name  string `json:"name"`
		Price int    `json:"price"`
	} `body:"json"`
}

func (in *gsCreateIn) Validate(context.Context) error {
	var v validate.Errors
	if in.Body.Name == "" {
		v.Add("body", "name", "is required")
	}
	return v.Err()
}

func (c *gsController) Create(_ context.Context, in gsCreateIn) (gsProduct, error) {
	return c.svc.Add(in.Body.Name, in.Body.Price), nil
}

var gsProductsModule = &dygo.Module{
	Name:        "products",
	Providers:   []dygo.Provider{dygo.Provide(newGSService)},
	Controllers: []dygo.Provider{dygo.Provide(newGSController)},
	Exports:     []dygo.Token{dygo.TypeOf[*gsService]()},
}

func TestGettingStarted(t *testing.T) {
	srv := dygotest.New(gsProductsModule).With(validate.Use(validate.Methods())).CompileHTTP(t)

	var p gsProduct
	srv.POST("/products").JSON(map[string]any{"name": "pen", "price": 2}).
		Do(t).Status(t, 201).JSON(t, &p)
	srv.GET("/products/"+strconv.Itoa(p.ID)).Do(t).Status(t, 200)
	srv.GET("/products/abc").Do(t).Status(t, 400)
	srv.GET("/products/99").Do(t).Status(t, 404)
	srv.POST("/products").JSON(map[string]any{"price": 2}).Do(t).Status(t, 400)
}

// FuzzGettingStartedCreate is the fuzz example of docs/recipes/testing.md:
// no body may produce a 5xx.
func FuzzGettingStartedCreate(f *testing.F) {
	srv := dygotest.New(gsProductsModule).With(validate.Use(validate.Methods())).CompileHTTP(f)
	f.Add(`{"name":"pen","price":2}`)
	f.Add(`{"price":"x"}`)
	f.Fuzz(func(t *testing.T, body string) {
		res := srv.POST("/products").Body(strings.NewReader(body), "application/json").Do(t)
		if res.StatusCode >= 500 {
			t.Fatalf("%d for %q", res.StatusCode, body)
		}
	})
}
