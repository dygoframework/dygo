package security

import (
	"context"
	"net/http"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/ratelimit"
)

// Controller serves /auth. Register, login, refresh and logout are public in
// Rules; me needs a token, as every route does by default.
type Controller struct {
	svc *Service
}

// NewController is the constructor.
func NewController(svc *Service) *Controller {
	return &Controller{svc: svc}
}

// Routes registers register, login, refresh, logout and me. Each client IP
// may call register, login and refresh 10 times a minute (429 after), which
// slows down password guessing.
func (c *Controller) Routes(r dygo.Router) {
	r = r.Prefix("/auth")
	limit := dygo.Guards(ratelimit.Guard(ratelimit.Options{Limit: 10, Window: time.Minute}))
	r.Post("/register", dygo.Handle(c.Register), limit)
	r.Post("/login", dygo.Handle(c.Login), dygo.HTTPCode(http.StatusOK), limit)
	r.Post("/refresh", dygo.Handle(c.Refresh), dygo.HTTPCode(http.StatusOK), limit)
	r.Post("/logout", dygo.Handle(c.Logout))
	r.Get("/me", dygo.HandleNoInput(c.Me))
}

// CredentialsIn is the input of register and login.
type CredentialsIn struct {
	Body Credentials `body:"json"`
}

// Register answers POST /auth/register with 201 and the new user.
func (c *Controller) Register(ctx context.Context, in CredentialsIn) (User, error) {
	return c.svc.Register(ctx, in.Body)
}

// Login answers POST /auth/login with an access token.
func (c *Controller) Login(ctx context.Context, in CredentialsIn) (TokenOut, error) {
	return c.svc.Login(ctx, in.Body)
}

// RefreshIn is the input of refresh and logout.
type RefreshIn struct {
	Body RefreshBody `body:"json"`
}

// Refresh answers POST /auth/refresh with a new access and refresh token.
// The refresh token sent is spent: sending it again ends the login.
func (c *Controller) Refresh(ctx context.Context, in RefreshIn) (TokenOut, error) {
	return c.svc.Refresh(ctx, in.Body)
}

// Logout answers POST /auth/logout with 204 and ends the login.
func (c *Controller) Logout(ctx context.Context, in RefreshIn) (dygo.NoContent, error) {
	return dygo.NoContent{}, c.svc.Logout(ctx, in.Body)
}

// Me answers GET /auth/me with the signed in user.
func (c *Controller) Me(ctx context.Context) (User, error) {
	return c.svc.Me(ctx)
}
