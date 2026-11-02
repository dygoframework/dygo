package lessons

import (
	"context"

	"github.com/dygoframework/dygo"

	"github.com/dygoframework/dygo/examples/academy/internal/modules/security"
)

// Controller maps /lessons to the service.
type Controller struct {
	svc *Service
}

// NewController is the constructor.
func NewController(svc *Service) *Controller {
	return &Controller{svc: svc}
}

// Routes registers the CRUD routes.
func (c *Controller) Routes(r dygo.Router) {
	r = r.Prefix("/lessons")
	r.Get("/", dygo.HandleNoInput(c.List))
	r.Get("/{id}", dygo.Handle(c.Get))
	r.Post("/", dygo.Handle(c.Create), security.Roles("admin"))
	r.Patch("/{id}", dygo.Handle(c.Update), security.Roles("admin"))
	r.Delete("/{id}", dygo.Handle(c.Delete), security.Roles("admin"))
}

// IDIn is the {id} path parameter.
type IDIn struct {
	ID int `path:"id"`
}

// CreateIn is the input of POST /lessons.
type CreateIn struct {
	Body CreateLesson `body:"json"`
}

// UpdateIn is the input of PATCH /lessons/{id}.
type UpdateIn struct {
	ID   int          `path:"id"`
	Body UpdateLesson `body:"json"`
}

// List answers GET /lessons.
func (c *Controller) List(ctx context.Context) ([]Lesson, error) {
	return c.svc.List(ctx), nil
}

// Get answers GET /lessons/{id}.
func (c *Controller) Get(ctx context.Context, in IDIn) (Lesson, error) {
	return c.svc.Get(ctx, in.ID)
}

// Create answers POST /lessons with 201.
func (c *Controller) Create(ctx context.Context, in CreateIn) (Lesson, error) {
	return c.svc.Create(ctx, in.Body), nil
}

// Update answers PATCH /lessons/{id}.
func (c *Controller) Update(ctx context.Context, in UpdateIn) (Lesson, error) {
	return c.svc.Update(ctx, in.ID, in.Body)
}

// Delete answers DELETE /lessons/{id} with 204.
func (c *Controller) Delete(ctx context.Context, in IDIn) (dygo.NoContent, error) {
	return dygo.NoContent{}, c.svc.Delete(ctx, in.ID)
}
