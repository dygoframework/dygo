package enrollments

import (
	"context"
	"strconv"

	"github.com/dygoframework/dygo"

	"github.com/dygoframework/dygo/examples/academy/internal/modules/security"
)

// Controller serves /enrollments for the signed in user.
type Controller struct {
	svc   *Service
	users *security.Service
}

// NewController is the constructor.
func NewController(svc *Service, users *security.Service) *Controller {
	return &Controller{svc: svc, users: users}
}

// Routes registers the routes. Every one needs a signed in user: no rule in
// security/rules.go opens /enrollments, and routes are locked by default.
func (c *Controller) Routes(r dygo.Router) {
	r = r.Prefix("/enrollments")
	r.Get("/", dygo.HandleNoInput(c.List))
	r.Post("/", dygo.Handle(c.Create))
	r.Delete("/{id}", dygo.Handle(c.Delete))
}

// IDIn is the {id} path parameter.
type IDIn struct {
	ID int `path:"id"`
}

// CreateIn is the input of POST /enrollments.
type CreateIn struct {
	Body CreateEnrollment `body:"json"`
}

// List answers GET /enrollments with the user's enrollments.
func (c *Controller) List(ctx context.Context) ([]Enrollment, error) {
	return c.svc.ForUser(ctx, userID(ctx)), nil
}

// Create answers POST /enrollments with 201.
func (c *Controller) Create(ctx context.Context, in CreateIn) (Enrollment, error) {
	u, err := c.users.Me(ctx)
	if err != nil {
		return Enrollment{}, err
	}
	return c.svc.Enroll(ctx, u.ID, u.Email, in.Body.CourseID)
}

// Delete answers DELETE /enrollments/{id} with 204.
func (c *Controller) Delete(ctx context.Context, in IDIn) (dygo.NoContent, error) {
	return dygo.NoContent{}, c.svc.Drop(ctx, userID(ctx), in.ID)
}

// userID is the signed in user's ID; the guard put the user on ctx.
func userID(ctx context.Context) int {
	p, _ := security.CurrentUser(ctx)
	id, _ := strconv.Atoi(p.Subject)
	return id
}
