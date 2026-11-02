package dygo_test

// This file is the code of the "Step by step: a CRUD controller" section in
// docs/guide-http.md, so the guide's examples are compiled and tested.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/errs"
)

type Book struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author"`
}

type BookStore struct {
	mu     sync.Mutex
	books  map[int]Book
	nextID int
}

func NewBookStore() *BookStore { return &BookStore{books: map[int]Book{}} }

func (s *BookStore) List(author string) []Book {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Book{}
	for _, b := range s.books {
		if author == "" || b.Author == author {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b Book) int { return a.ID - b.ID })
	return out
}

func (s *BookStore) Get(id int) (Book, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.books[id]
	return b, ok
}

func (s *BookStore) Add(body BookBody) Book {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	b := Book{ID: s.nextID, Title: body.Title, Author: body.Author}
	s.books[b.ID] = b
	return b
}

func (s *BookStore) Replace(id int, body BookBody) (Book, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.books[id]; !ok {
		return Book{}, false
	}
	b := Book{ID: id, Title: body.Title, Author: body.Author}
	s.books[id] = b
	return b, true
}

func (s *BookStore) Save(b Book) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.books[b.ID] = b
}

func (s *BookStore) Remove(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.books[id]
	delete(s.books, id)
	return ok
}

// --- the controller, as in the guide ---

type BooksController struct{ store *BookStore }

func NewBooksController(s *BookStore) *BooksController { return &BooksController{store: s} }

func (c *BooksController) Routes(r dygo.Router) {
	r = r.Prefix("/books")
	r.Get("/", dygo.Handle(c.List))          // GET    /books?author=...
	r.Get("/{id}", dygo.Handle(c.Get))       // GET    /books/1
	r.Post("/", dygo.Handle(c.Create))       // POST   /books        -> 201
	r.Put("/{id}", dygo.Handle(c.Replace))   // PUT    /books/1      -> 200
	r.Patch("/{id}", dygo.Handle(c.Update))  // PATCH  /books/1      -> 200
	r.Delete("/{id}", dygo.Handle(c.Delete)) // DELETE /books/1      -> 204
}

var BooksModule = &dygo.Module{
	Name:        "books",
	Providers:   []dygo.Provider{dygo.Provide(NewBookStore)},
	Controllers: []dygo.Provider{dygo.Provide(NewBooksController)},
}

type ListBooksIn struct {
	Author string `query:"author"` // optional: "" when missing
}

func (c *BooksController) List(_ context.Context, in ListBooksIn) ([]Book, error) {
	return c.store.List(in.Author), nil
}

type BookIDIn struct {
	ID int `path:"id"` // "abc" is a 400 before the handler runs
}

func (c *BooksController) Get(_ context.Context, in BookIDIn) (Book, error) {
	b, ok := c.store.Get(in.ID)
	if !ok {
		return Book{}, errs.NotFound(fmt.Sprintf("book %d not found", in.ID))
	}
	return b, nil
}

type BookBody struct {
	Title  string `json:"title"`
	Author string `json:"author"`
}

type CreateBookIn struct {
	Body BookBody `body:"json"`
}

func (c *BooksController) Create(_ context.Context, in CreateBookIn) (Book, error) {
	if in.Body.Title == "" {
		return Book{}, errs.BadRequest("title is required")
	}
	return c.store.Add(in.Body), nil // POST answers 201 Created
}

type ReplaceBookIn struct {
	ID   int      `path:"id"`
	Body BookBody `body:"json"`
}

func (c *BooksController) Replace(_ context.Context, in ReplaceBookIn) (Book, error) {
	b, ok := c.store.Replace(in.ID, in.Body)
	if !ok {
		return Book{}, errs.NotFound(fmt.Sprintf("book %d not found", in.ID))
	}
	return b, nil // 200
}

type UpdateBookIn struct {
	ID   int `path:"id"`
	Body struct {
		Title  *string `json:"title"`
		Author *string `json:"author"`
	} `body:"json"`
}

func (c *BooksController) Update(_ context.Context, in UpdateBookIn) (Book, error) {
	b, ok := c.store.Get(in.ID)
	if !ok {
		return Book{}, errs.NotFound(fmt.Sprintf("book %d not found", in.ID))
	}
	if in.Body.Title != nil {
		b.Title = *in.Body.Title
	}
	if in.Body.Author != nil {
		b.Author = *in.Body.Author
	}
	c.store.Save(b)
	return b, nil
}

func (c *BooksController) Delete(_ context.Context, in BookIDIn) (dygo.NoContent, error) {
	if !c.store.Remove(in.ID) {
		return dygo.NoContent{}, errs.NotFound(fmt.Sprintf("book %d not found", in.ID))
	}
	return dygo.NoContent{}, nil // 204, empty body
}

func TestGuideCRUD(t *testing.T) {
	srv := dygotest.New(BooksModule).CompileHTTP(t)

	var b Book
	srv.POST("/books").JSON(BookBody{Title: "Dune", Author: "Herbert"}).Do(t).Status(t, http.StatusCreated).JSON(t, &b)
	if b.ID != 1 || b.Title != "Dune" {
		t.Fatalf("created %+v", b)
	}
	srv.POST("/books").JSON(BookBody{}).Do(t).Status(t, http.StatusBadRequest)
	srv.POST("/books").Body(strings.NewReader("title=Dune"), "text/plain").Do(t).Status(t, http.StatusBadRequest)
	srv.POST("/books").Body(strings.NewReader("{bad json"), "application/json").Do(t).Status(t, http.StatusBadRequest)

	var list []Book
	srv.GET("/books?author=Herbert").Do(t).Status(t, 200).JSON(t, &list)
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}
	srv.GET("/books?author=Nobody").Do(t).Status(t, 200).JSON(t, &list)
	if len(list) != 0 {
		t.Fatalf("filtered list %+v", list)
	}

	srv.GET("/books/1").Do(t).Status(t, 200)
	srv.GET("/books/abc").Do(t).Status(t, 400)
	srv.GET("/books/9").Do(t).Status(t, 404)

	srv.PUT("/books/1").JSON(BookBody{Title: "Dune Messiah", Author: "Herbert"}).Do(t).Status(t, 200).JSON(t, &b)
	if b.Title != "Dune Messiah" {
		t.Fatalf("replaced %+v", b)
	}
	srv.PATCH("/books/1").JSON(map[string]string{"author": "Frank Herbert"}).Do(t).Status(t, 200).JSON(t, &b)
	if b.Title != "Dune Messiah" || b.Author != "Frank Herbert" {
		t.Fatalf("patched %+v", b)
	}

	res := srv.DELETE("/books/1").Do(t).Status(t, http.StatusNoContent)
	if res.Text() != "" {
		t.Fatalf("204 body %q", res.Text())
	}
	srv.DELETE("/books/1").Do(t).Status(t, 404)
}
