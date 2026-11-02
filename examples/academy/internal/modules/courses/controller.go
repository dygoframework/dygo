package courses

import (
	"context"

	"github.com/dygoframework/dygo"

	"github.com/dygoframework/dygo/examples/academy/internal/modules/security"
)

// Controller maps /courses to the service.
type Controller struct {
	svc *Service
}

// NewController is the constructor.
func NewController(svc *Service) *Controller {
	return &Controller{svc: svc}
}

// Routes registers the CRUD routes.
func (c *Controller) Routes(r dygo.Router) {
	r = r.Prefix("/courses")
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

// CreateIn is the input of POST /courses.
type CreateIn struct {
	Body CreateCourse `body:"json"`
}

// UpdateIn is the input of PATCH /courses/{id}.
type UpdateIn struct {
	ID   int          `path:"id"`
	Body UpdateCourse `body:"json"`
}

// List answers GET /courses.
func (c *Controller) List(ctx context.Context) ([]Course, error) {
	return c.svc.List(ctx), nil
}

// Get answers GET /courses/{id}.
func (c *Controller) Get(ctx context.Context, in IDIn) (Course, error) {
	return c.svc.Get(ctx, in.ID)
}

// Create answers POST /courses with 201.
func (c *Controller) Create(ctx context.Context, in CreateIn) (Course, error) {
	return c.svc.Create(ctx, in.Body), nil
}

// Update answers PATCH /courses/{id}.
func (c *Controller) Update(ctx context.Context, in UpdateIn) (Course, error) {
	return c.svc.Update(ctx, in.ID, in.Body)
}

// Delete answers DELETE /courses/{id} with 204.
func (c *Controller) Delete(ctx context.Context, in IDIn) (dygo.NoContent, error) {
	return dygo.NoContent{}, c.svc.Delete(ctx, in.ID)
}
