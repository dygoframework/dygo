# Recipe: security

`dygo g security` makes an API secure by default: every route needs
a valid bearer token, and you open routes on purpose. The code is generated
into your app, uses only the standard library, and is yours to read and
change.

```sh
dygo new shop -security            # a new project, secure from the start
dygo g security                    # or in an existing project
dygo g security -oidc              # tokens from Keycloak, Auth0, Okta, Entra ID...
```

It writes one `security/` package (`src/modules/security/` or
`internal/modules/security/` in a modular project), imports it in the root
module, adds `security.CORS()` to the app's middleware, adds the settings to
`.env` and `.env.example`, and sets `"security": true` in `dygo.json` so later
generators write protected routes.

[`examples/academy`](https://github.com/dygoframework/dygo/tree/main/examples/academy)
is a full app built this way.

## What you get

| Route | Does |
|---|---|
| `POST /auth/register` | `{"email","password"}`, 201 with the user, 409 if the email is taken |
| `POST /auth/login` | 200 with `{"accessToken","tokenType","expiresAt","refreshToken"}`, 401 for a wrong email or password |
| `POST /auth/refresh` | `{"refreshToken"}`, 200 with a new pair; the old refresh token is spent |
| `POST /auth/logout` | `{"refreshToken"}`, 204; the login ends |
| `GET /auth/me` | the signed in user |
| any other route | 401 without a valid token |

```
GET  /orders                401  {"statusCode":401,"error":"Unauthorized","message":"missing bearer token"}
GET  /orders   (token)      200
DELETE /orders/1 (user)     403  {"statusCode":403,"error":"Forbidden","message":"access denied"}
```

A 401 carries `WWW-Authenticate: Bearer realm="api"`. A 403 never says which
role was missing. The reason a token was refused goes to the debug log, never
to the client.

## Open routes and require roles

There are three places to decide who may call what, from the broadest to the
narrowest. All three can be used together.

**1. Rules** in `security/rules.go`.
They are checked top to bottom, and the first match wins:

```go
func Rules(cfg *Config) []Rule {
    rules := []Rule{
        Path("/").PermitAll(),
        Path("/health/**").PermitAll(),
        Path("/auth/register", "/auth/login", "/auth/refresh", "/auth/logout").PermitAll(),
        Path("/admin/**").HasRole("admin"),
        Method("GET").Path("/courses", "/courses/**").PermitAll(),
        Method("POST", "PUT", "PATCH", "DELETE").Path("/products/**").HasAuthority("products:write"),
    }
    if !cfg.Production() {
        rules = append(rules, Path("/docs", "/openapi.json").PermitAll())
    }
    return append(rules, AnyRequest().Authenticated())
}
```

Patterns are route paths, as registered (with the global prefix if you set
one): `*` is one segment and `**` any number. A route that no rule matches
needs a signed in user, because the guard adds `AnyRequest().Authenticated()`
at the end even if you remove it.

**2. Route options**, like `@PreAuthorize` on a controller method:

```go
func (c *Controller) Routes(r dygo.Router) {
    r = r.Prefix("/orders")
    r.Get("/", dygo.HandleNoInput(c.List))                                // a signed in user
    r.Get("/catalog", dygo.HandleNoInput(c.Catalog), security.Public())    // anyone
    r.Delete("/{id}", dygo.Handle(c.Delete), security.Roles("admin"))      // admins
    r.Put("/{id}", dygo.Handle(c.Update), security.Authorities("orders:write"))
}
```

`Public()` also marks the route as public in the OpenAPI document. On
`r.Use(...)` an option applies to the whole controller.

`dygo g security` also sets `BearerAuth: true` in the app's `openapi.Options`,
so `/docs` shows an **Authorize** button. Log in with `POST /auth/login`, paste
the `accessToken` into Authorize (without the `Bearer ` prefix), and every
"Try it out" call sends it. Routes marked `Public()` show no lock.

**3. Checks in code**, for decisions that need data:

```go
func (s *Service) Cancel(ctx context.Context, id int) error {
    if err := security.Require(ctx, security.HasAuthority("orders:cancel")); err != nil {
        return err // 401 or 403
    }
    order, err := s.repo.Find(ctx, id)
    if err != nil {
        return err
    }
    if err := security.RequireOwner(ctx, order.UserID); err != nil {
        return err // 403 unless it is the caller's order, or the caller is an admin
    }
    ...
}
```

`p, ok := security.CurrentUser(ctx)` returns the signed in user anywhere
below the guard: `p.Subject` (the user ID), `p.Email`, `p.Roles`,
`p.Authorities`.

## Roles and authorities

`security/roles.go` holds the role hierarchy and what each role grants:

```go
var Hierarchy = map[string][]string{
    "admin": {"user"}, // an admin passes HasRole("user") too
}

var RoleAuthorities = map[string][]string{
    "admin": {"orders:write", "orders:cancel"},
}
```

`RequireOwner` lets `OwnerBypassRole` (`"admin"`) through; set it to `""` to
make owners the only ones allowed.

## Tokens and sessions

- **Access token:** an HS256 JWT signed with `AUTH_SECRET`, valid for
  `AUTH_TOKEN_TTL` (15 minutes). The header must be exactly HS256, so
  `"alg": "none"` and algorithm swaps are refused. `iss`, `exp` and `iat` are
  checked with 30 seconds of clock skew.
- **Refresh token:** 32 random bytes, stored only as their SHA-256, valid for
  `AUTH_REFRESH_TTL` (30 days). Each refresh spends the token and returns a
  new one. Sending a spent token again ends the whole login, for the user and
  for whoever stole the copy.
- **Logout** ends the login. Access tokens that were already issued work until
  they expire, which is why they are short.
- **Passwords:** PBKDF2-HMAC-SHA256 with 600,000 iterations. A hash made with
  fewer iterations is upgraded at the next login. Login takes the same time
  for an unknown email and for a wrong password.
- **Rate limit:** register, login and refresh allow 10 calls a minute per
  client IP, then answer 429.
- **Speed:** a token that passed every check is remembered until it expires,
  so a request with a known token costs well under a microsecond more than
  one without security, with own tokens or `-oidc`.

## Store users in a database

Users and refresh tokens are stored where `DATABASE_URL` says. With
`memory://` they are kept in memory and lost on restart. With Postgres or
MySQL they are stored in the `users`, `refresh_tokens` and
`refresh_families` tables:

```sh
dygo db init -driver postgres      # sets DATABASE_URL in .env
dygo g migration                   # writes the three tables
# add to the migration: CREATE UNIQUE INDEX users_email ON users (email);
dygo db migrate
```

Switching from memory to the database is a change to `.env`, not to code.
`UserStore` in `security/users.go` is an interface, so you can also put users anywhere else.

## Login at an identity provider (`-oidc`)

When users sign in at Keycloak, Auth0, Okta, Entra ID or Google, the API only
checks their tokens, as an OAuth2 resource server:

```sh
dygo g security -oidc
```

```sh
OAUTH_ISSUER_URL=https://auth.example.com/realms/shop
OAUTH_AUDIENCE=shop-api
OAUTH_ROLES_CLAIM=realm_access.roles      # Keycloak; the default is roles
```

At startup the app reads `<issuer>/.well-known/openid-configuration` and the
provider's keys (JWKS). It accepts RS256 and ES256 tokens from that issuer for
that audience, and checks `exp` and `nbf`. Roles come from
`OAUTH_ROLES_CLAIM`, the token's scopes (`scope` or `scp`) become
authorities, and `p.Claims` has every claim. When the provider rotates its
keys, the first token with a new key makes the app fetch the keys again.

There are no register, login, refresh or logout routes in this mode;
`GET /auth/me` shows what the token says. Rules, route options, `Require` and
the generators work the same. In production the issuer must use https and
`OAUTH_AUDIENCE` must be set, or the app does not start.

## Browsers (CORS)

`security.CORS()` allows the origins in `CORS_ALLOWED_ORIGINS` (comma
separated) and none when it is empty. Tokens travel in the `Authorization`
header, not in cookies, so there is no CSRF to defend against.

## Generators in a security project

After `dygo g security`, generators write protected code with no extra flag:

| Command | Routes |
|---|---|
| `dygo g resource products` | list and get need a signed in user; create, update and delete need `admin` |
| `dygo g resource posts -public-read` | list and get are public |
| `dygo g crud Invoice -write-role accountant` | writes need `accountant`; `-write-role -` means any signed in user |
| `dygo g controller billing` | a signed in user, unless a rule opens them |
| `dygo g controller hooks -public` | public |

Each command prints one line saying who may call the new routes, and the
generated tests sign in and check the 401 and 403 answers.

## Test protected routes

`securitytest` signs in without a real login:

```go
func TestOrders(t *testing.T) {
    securitytest.Setup(t) // before building the app
    srv := dygotest.New(&dygo.Module{Name: "test", Imports: []*dygo.Module{security.Module, orders.Module}}).
        CompileHTTP(t)

    srv.GET("/orders").Do(t).Status(t, 401)
    srv.GET("/orders").With(securitytest.User("7", "user")).Do(t).Status(t, 200)
    srv.DELETE("/orders/1").With(securitytest.User("7", "user")).Do(t).Status(t, 403)
    srv.DELETE("/orders/1").With(securitytest.Admin()).Do(t).Status(t, 204)
    srv.PUT("/orders/1").With(securitytest.As(security.Principal{Subject: "7", Authorities: []string{"orders:write"}})).
        Do(t).Status(t, 200)
}

func TestCancel(t *testing.T) {
    ctx := securitytest.Context(context.Background(), security.Principal{Subject: "7", Roles: []string{"user"}})
    err := svc.Cancel(ctx, 1) // a service test, no HTTP
    ...
}
```

The helpers sign real tokens, so the guard is tested for real. With `-oidc`,
`securitytest.Setup` starts a fake identity provider with its own key, and the
same helpers work.

## Coming from `dygo g auth`

An app uses `g auth` or `g security`, not both; `g security` stops if it finds
an `auth/` package. To switch:

1. Delete `auth/` and its import in the root module.
2. Run `dygo g security`.
3. Remove `auth.Required()` (routes are locked by default). Replace
   `auth.Roles` with `security.Roles` and `auth.UserFrom(ctx)` with
   `security.CurrentUser(ctx)`; the user ID is `p.Subject`. Open the public
   routes in `rules.go` or with `security.Public()`.
4. In tests, replace logins or `auth.BearerFor` with `securitytest.Setup(t)`
   and `.With(securitytest.Admin())`.

`go build` points at every line to change. `examples/academy` was moved this
way.

## Not included

A stateless API does not need form login, sessions, remember-me cookies,
CSRF tokens or login pages, so they are left out. Also not included: an authorization server (use your identity provider), DPoP and PAR.
