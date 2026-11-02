# Recipe: authentication

This recipe is `dygo g auth`: login and tokens, with routes protected one by
one. For an API that is locked by default, with rules, refresh tokens, an
identity provider and test helpers, use `dygo g security` instead: see the
[security recipe](security.md).

The quickest path is the generator. It writes a complete, tested auth module
that uses only the standard library, which you then own and change.

```sh
dygo g auth
```

You get `auth/` (or `src/modules/auth/` in a modular project), imported
in the root module:

| Route | Does |
|---|---|
| `POST /auth/register` | `{"email","password"}`, 201 with the user, 409 if the email is taken |
| `POST /auth/login` | 200 with `{"accessToken","tokenType":"Bearer","expiresAt"}`, 401 for a wrong email or password |
| `GET /auth/me` | the signed in user, 401 without a valid token |

Passwords are hashed with PBKDF2-HMAC-SHA256 and tokens are HS256 JWTs. Set
`AUTH_SECRET` (32 or more characters, `openssl rand -hex 32`); it is required
when `APP_ENV=production`.

## Protect routes

The module is global, so any controller can use its route options without
importing the module:

```go
func (c *Controller) Routes(r dygo.Router) {
    r = r.Prefix("/orders").Use(auth.Required())           // every route of the controller
    r.Get("/", dygo.HandleNoInput(c.List))
    r.Delete("/{id}", dygo.Handle(c.Delete), auth.Roles("admin")) // and this one needs a role
}

func (c *Controller) List(ctx context.Context) ([]Order, error) {
    claims, _ := auth.UserFrom(ctx) // set by the guard
    return c.svc.ListFor(ctx, claims.UserID())
}
```

Without a token the client gets 401; with a token but without the role, 403.

## How it works

The generated guard is ordinary Dygo code you can read in `auth/guard.go`:

```go
var rolesKey = meta.NewKey[[]string]("auth.roles")

// Required protects a route, or every route of a controller on Router.Use.
func Required() dygo.RouteOption { return dygo.Guards(dygo.Ref[*Guard]()) }

// Roles protects a route and requires one of roles.
func Roles(roles ...string) dygo.RouteOption {
    return dygo.Options(Required(), rolesKey.Set(roles))
}

func (g *Guard) CanActivate(ec dygo.ExecutionContext) (bool, error) {
    token, ok := strings.CutPrefix(ec.HTTP().Request().Header.Get("Authorization"), "Bearer ")
    if !ok {
        return false, errs.Unauthorized("missing bearer token")
    }
    c, err := g.tokens.Verify(token)
    if err != nil {
        return false, errs.Unauthorized(err.Error())
    }
    ec.SetContext(context.WithValue(ec.Context(), claimsKey{}, c))
    want := meta.GetAllAndMerge(ec, rolesKey)
    if len(want) == 0 || slices.ContainsFunc(want, func(r string) bool { return slices.Contains(c.Roles, r) }) {
        return true, nil
    }
    return false, nil
}
```

- `dygo.Ref[*Guard]()` makes the guard a provider, so it can inject the token
  service, and it is built once at boot.
- Roles are typed route metadata. `GetAllAndMerge` joins the controller's and
  the route's values, like `Reflector.getAllAndMerge`.
- The claims travel on the request context, which is how Go passes request
  values; the handler reads them with `auth.UserFrom(ctx)`.

## Make it secure by default

`dygo g security` does this for you, with rules and tests
([security recipe](security.md)). By hand: to require a token everywhere and opt routes out, flip the logic: register the
guard globally and mark public routes with metadata.

```go
var publicKey = meta.NewKey[bool]("auth.public")

func Public() dygo.RouteOption { return publicKey.Set(true) }

func (g *Guard) CanActivate(ec dygo.ExecutionContext) (bool, error) {
    if public, _ := publicKey.Get(ec); public {
        return true, nil
    }
    // ... the token check from above
}

// in the auth module's Providers:
dygo.Provide(NewGuard, dygo.Group(dygo.AppGuards)) // like APP_GUARD
```

Remember the health routes: `health.Module(health.Options{Use: []dygo.RouteOption{auth.Public()}})`.

## Store users in a database

Users are kept in memory in `auth/users.go`. Replace the `Users` type with a
repository over your database (see the [SQL recipe](sql.md)); the service only
calls its methods.

## Use an external identity provider

`dygo g security -oidc` generates this with the standard library
([security recipe](security.md#login-at-an-identity-provider-oidc)). By hand: for OIDC (Auth0, Keycloak, Google, Entra ID), keep the guard and replace
`Tokens.Verify` with verification against the provider's JWKS, for example
with [github.com/coreos/go-oidc](https://github.com/coreos/go-oidc). The
route options, the roles metadata and `UserFrom` stay the same, so no
controller changes.

## Test protected routes

A module whose routes use `auth.Required()` needs the auth module in its
tests, or the boot fails with `*auth.Guard is not visible`:

```go
srv := dygotest.New(&dygo.Module{Name: "app", Imports: []*dygo.Module{auth.Module, orders.Module}}).
    CompileHTTP(t)

var tok struct{ AccessToken string }
srv.POST("/auth/register").JSON(creds).Do(t).Status(t, 201)
srv.POST("/auth/login").JSON(creds).Do(t).Status(t, 200).JSON(t, &tok)
srv.GET("/orders").Header("Authorization", "Bearer "+tok.AccessToken).Do(t).Status(t, 200)
srv.GET("/orders").Do(t).Status(t, 401)
```
