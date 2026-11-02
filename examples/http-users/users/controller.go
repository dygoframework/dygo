package users

import (
	"context"
	"errors"
	"iter"
	"strings"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/examples/http-users/auth"
	"github.com/dygoframework/dygo/openapi"
	"github.com/dygoframework/dygo/validate"
)

// Controller maps HTTP routes to the service. It is a singleton; the request
// scoped RequestMeta is reached through dygo.Scoped.
type Controller struct {
	svc  *Service
	meta dygo.Scoped[*RequestMeta]
}

// NewController is the constructor.
func NewController(svc *Service, meta dygo.Scoped[*RequestMeta]) *Controller {
	return &Controller{svc: svc, meta: meta}
}

// Routes registers the controller's routes. With URI versioning and default
// version 1 they are served under /api/v1/users; GetV2 is /api/v2/users/{id}.
// Every route needs a signed in user (the global auth guard); changes need
// the admin role.
func (c *Controller) Routes(r dygo.Router) {
	r = r.Prefix("/users").Use(auth.Roles("user"), openapi.Tags("users"),
		openapi.Response(401, "Missing or invalid token"), openapi.Response(403, "Missing role"))
	r.Get("/", dygo.Handle(c.List), openapi.Summary("List users"))
	r.Get("/events", dygo.Handle(c.Events), openapi.Summary("Stream user changes (server-sent events)"))
	r.Get("/{id}", dygo.Handle(c.Get), openapi.Summary("Get a user"), openapi.Response(404, "No such user"))
	r.Get("/{id}", dygo.Handle(c.GetV2), dygo.Version("2"), openapi.Summary("Get a user with a split name"),
		openapi.Response(404, "No such user"))

	admin := r.Use(auth.Roles("admin"))
	admin.Post("/", dygo.Handle(c.Create), dygo.StrictJSON(), openapi.Summary("Create a user"),
		openapi.Response(409, "Email already registered"))
	admin.Patch("/{id}", dygo.Handle(c.Rename), openapi.Summary("Rename a user"), openapi.Response(404, "No such user"))
	admin.Delete("/{id}", dygo.Handle(c.Delete), openapi.Summary("Delete a user"), openapi.Response(404, "No such user"))
}

// Pagination is embedded in list inputs.
type Pagination struct {
	Offset int `query:"offset" default:"0"`
	Limit  int `query:"limit"  default:"20"`
}

// Validate is called by the global validation pipe (validate.Methods).
func (p Pagination) Validate(context.Context) error {
	var v validate.Errors
	if p.Offset < 0 {
		v.Add("query", "offset", "must not be negative")
	}
	if p.Limit < 1 || p.Limit > 100 {
		v.Add("query", "limit", "must be between 1 and 100")
	}
	return v.Err()
}

// ListIn is the List input.
type ListIn struct {
	Pagination
}

// Page is a page of results.
type Page[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

// List returns a page of users.
func (c *Controller) List(ctx context.Context, in ListIn) (Page[User], error) {
	items, total := c.svc.List(ctx, in.Offset, in.Limit)
	return Page[User]{Items: items, Total: total}, nil
}

// IDIn reads the user ID from the path.
type IDIn struct {
	ID ID `path:"id"`
}

// Get returns one user.
func (c *Controller) Get(ctx context.Context, in IDIn) (User, error) {
	u, err := c.svc.Get(ctx, in.ID)
	return u, mapErr(err)
}

// UserV2 is the version 2 shape: the name is split.
type UserV2 struct {
	ID        ID     `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

// GetV2 returns one user in the version 2 shape.
func (c *Controller) GetV2(ctx context.Context, in IDIn) (UserV2, error) {
	u, err := c.svc.Get(ctx, in.ID)
	if err != nil {
		return UserV2{}, mapErr(err)
	}
	first, last, _ := strings.Cut(u.Name, " ")
	return UserV2{ID: u.ID, Email: u.Email, FirstName: first, LastName: last}, nil
}

// NewUser is the Create body.
type NewUser struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Validate checks the body; the validation pipe finds it on the Body field.
// With the validate/playground adapter this would be validate:"..." tags.
func (u *NewUser) Validate(context.Context) error {
	var v validate.Errors
	if !strings.Contains(u.Email, "@") {
		v.Add("body", "email", "must be a valid email")
	}
	if strings.TrimSpace(u.Name) == "" {
		v.Add("body", "name", "is required")
	}
	return v.Err()
}

// CreateIn is the Create input.
type CreateIn struct {
	Body NewUser `body:"json"`
}

// Create registers a user and answers 201 with the request ID header.
func (c *Controller) Create(ctx context.Context, in CreateIn) (User, error) {
	meta, err := c.meta.Get(ctx)
	if err != nil {
		return User{}, err
	}
	dygo.SetHeader(ctx, "X-Request-ID", meta.RequestID)
	u, err := c.svc.Create(ctx, in.Body.Email, in.Body.Name)
	return u, mapErr(err)
}

// RenameIn is the Rename input.
type RenameIn struct {
	ID   ID `path:"id"`
	Body struct {
		Name string `json:"name"`
	} `body:"json"`
}

// Rename changes a user's name.
func (c *Controller) Rename(ctx context.Context, in RenameIn) (User, error) {
	u, err := c.svc.Rename(ctx, in.ID, in.Body.Name)
	return u, mapErr(err)
}

// Delete removes a user.
func (c *Controller) Delete(ctx context.Context, in IDIn) (dygo.NoContent, error) {
	return dygo.NoContent{}, mapErr(c.svc.Delete(ctx, in.ID))
}

// EventsIn is the Events input.
type EventsIn struct {
	Max int `query:"max"` // stop after this many events; 0 means no limit
}

// Events streams user changes as server-sent events.
func (c *Controller) Events(ctx context.Context, in EventsIn) (dygo.Responder, error) {
	ch := c.svc.Subscribe(ctx)
	stopping := dygo.Stopping(ctx) // end the stream promptly on shutdown
	seq := iter.Seq[Event](func(yield func(Event) bool) {
		for n := 0; in.Max == 0 || n < in.Max; n++ {
			select {
			case <-ctx.Done():
				return
			case <-stopping:
				return
			case e, ok := <-ch:
				if !ok || !yield(e) {
					return
				}
			}
		}
	})
	return dygo.SSE(seq), nil
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return errs.NotFound("user not found", errs.Code("USER_NOT_FOUND"))
	case errors.Is(err, ErrEmailTaken):
		return errs.Conflict("email already registered", errs.Code("EMAIL_TAKEN"))
	}
	return err
}
