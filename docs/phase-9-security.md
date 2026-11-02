# Phase 9: Security (`dygo g security`)

**Goal:** one command that makes a Dygo API secure by default. After
`dygo g security`, every route needs a valid token, and you open routes on
purpose. The code is generated into the app, so you can read it and change it.

**Status (2026-11-02):** Dylan approved the plan with the recommended answers
to section 16. Milestone 1 (core security layer) is built: `dygo g security`,
`dygo new -security`, the `security/` package, `securitytest` and
`dygotest` `Req.With`. Decisions D69 to D72 are in `docs/decisions.md`.
Milestone 2 (generators in a security project, D73) is built too:
`"security": true` in `dygo.json`, `-public`, `-public-read`, `-write-role`,
and generated tests that sign in. Milestone 3 (token lifecycle, D74) is
built: refresh tokens with rotation and reuse detection, `POST /auth/refresh`
and `POST /auth/logout`, 15 minute access tokens, password rehash on login,
rate limits on register, login and refresh, and users and refresh tokens
stored where `DATABASE_URL` says. Milestone 4 (OAuth2 resource server, D75)
is built: `dygo g security -oidc` and `dygo new -security -oidc` write a
`security/` package that checks the tokens of an identity provider (OIDC
discovery, JWKS, RS256 and ES256, `iss`/`aud`/`exp`/`nbf`, roles from
`OAUTH_ROLES_CLAIM`, scopes as authorities), and `securitytest` runs a fake
provider so the same test helpers work. Milestone 5 is done:
[`docs/recipes/security.md`](recipes/security.md) (setup, rules, roles,
tokens, OIDC, testing, and the `g auth` switch) and
`examples/academy` moved from `g auth` to `g security`. Phase 9 is complete.

Performance pass (2026-11-02, D76), measured on a generated app (GET with a
token; the same route without security takes about 1.3 µs):

| | before | after |
|---|---|---|
| own tokens, request with a token | 6.2 µs, 30 allocs | 1.7 µs, 16 allocs |
| OIDC, request with a token | 52 µs, 58 allocs | 2.0 µs, 16 allocs |
| public route | +0.35 µs | +0.35 µs, one alloc fewer |

Checked tokens are kept in a bounded cache (16 x 1024) until they expire,
so the signature is checked once per token. Refresh spends a token with one
conditional `UPDATE ... WHERE used = false` instead of a lock, so refreshes
no longer wait for each other and two app instances cannot both accept the
same token. Provider keys are fetched outside the verifier's lock. The rule
cache is a copy-on-write map read without a lock.

Found while moving `examples/academy` (milestone 5): an existing project
without `dygo/sql` did not build after `dygo g security`, because users and
refresh tokens are stored through it since milestone 3. `g security` now adds
`sql.Module(nil)` to the root module and the `dygo/sql` requirement to
`go.mod`, as `g crud` does; `-oidc` stores nothing and skips it. The academy
move took the four steps in the recipe, plus `admin.go` for the first admin.

Built differently from this plan, in milestone 1: `Rules` takes the config
(`Rules(cfg *Config)`) so the docs rule can depend on `APP_ENV`; `/` is
public by default because `dygo new` serves a welcome message there;
`UserStore` methods return an error for database stores.

Built differently in milestone 2: the `dygo routes` ACCESS column is not
built. The CLI cannot evaluate the app's rules without running them, so it
moves to a later milestone (an inspect hook that asks the guard). `dygo g
test` route tests boot only the feature module, so they are unchanged.

Built differently in milestone 3: the rate limit is 10 a minute per client
IP on register, login and refresh, not 5 keyed by IP plus email, because
guards run before the body is read and the email is not known yet. Access
tokens still check `iss`, `exp` and `iat` only; `aud` and `nbf` come with
the OAuth2 resource server (milestone 4), where tokens from other apps
appear. `dygo g migration` now also finds structs given to
`sql.NewRepository[E]` in the file that declares them, so it writes the
`users`, `refresh_tokens` and `refresh_families` tables; the unique index on
`users.email` is added by hand (the hint after `g security` says so). With
several app instances, two requests racing with the same refresh token can
both succeed; the next reuse still revokes the login (fixed in the
performance pass: the token is now spent with a conditional update).

Built differently in milestone 4: `-oidc` is a separate file set, not a
switch at run time, so an OIDC app has no users, passwords or token issuing
code at all; it keeps `GET /auth/me`, which returns what the token says.
The verifier lives in `security/verifier.go` (no separate `jwks.go`). An
unknown `kid` fetches the keys again at most once a minute, but the first
fetch after boot is not held back, so a key rotated just after the app
starts still works. Without `OAUTH_ISSUER_URL` outside production the app
starts and refuses every token; in production it needs an https issuer and
`OAUTH_AUDIENCE`. ES256 supports the P-256 curve; other algorithms (RS384,
PS256, EdDSA) are refused for now.

**Scope agreed with Dylan (2026-11-02):**

- Two generators. `dygo new` is unchanged. `dygo g auth` stays the small
  version (login, JWT, `Required()`, `Roles()`). `dygo g security` is new.
- **One package** (Dylan's pick, 2026-11-02): authentication and
  authorization live together, so `g security` writes a single `security/` package with both, including the
  login, register, refresh and logout routes. Apps import one name. An app
  uses `g auth` or `g security`, not both: `g security` stops with a message
  if `auth/` exists (section 7 explains the switch).
- A stateless REST **API** (a resource server), not a server-rendered app. No
  form login, sessions, remember-me cookies, CSRF or login pages.
- Keep it simple: standard library only, and plain Go function calls instead
  of annotations or an expression language.

## 1. What a user gets

```sh
dygo new shop -security          # simple layout, secure from the start
dygo new shop -t modular -security
# or, in an existing project:
dygo g security                  # one package: login, tokens, rules and checks
dygo start
```

| Command | What it does |
|---|---|
| `dygo new <name> -security` | `dygo new`, then `g security` |
| `dygo g security` | writes `security/`, imports it in the root module, adds the `.env` block, sets `"security": true` in `dygo.json` |
| `dygo g security -oidc` | the same, set up for an external identity provider (no login routes) |
| `dygo g controller\|crud\|resource ... -public` | in a security project, makes the new routes public |
| `dygo g crud\|resource ... -public-read` | list and get public, writes protected |
| `dygo g crud\|resource ... -write-role <role>` | role for create, update and delete (default `admin`) |

```
GET  /health/live        200   public by default
POST /auth/login         200   public
GET  /orders             401   {"status":401,"error":"Unauthorized","message":"missing bearer token"}
GET  /orders  (token)    200
DELETE /orders/1 (user)  403   {"status":403,"error":"Forbidden","message":"access denied"}
```

Files added (flat project; modular projects get `src/modules/security/`):

| File | What it is |
|---|---|
| `security/module.go` | the module, global guard, middleware wiring |
| `security/rules.go` | the ordered rule list you edit |
| `security/guard.go` | the global guard that applies the rules |
| `security/principal.go` | `Principal`, `CurrentUser(ctx)` |
| `security/authorize.go` | `Require`, `RequireOwner`, checks |
| `security/roles.go` | role hierarchy, authorities |
| `security/verifier.go` | token verification: own tokens or OIDC |
| `security/jwks.go` | JWKS fetch and cache, RS256/ES256 |
| `security/errors.go` | JSON 401 and 403 |
| `security/config.go` | settings from `.env` |
| `security/controller.go` | `/auth/register`, `login`, `refresh`, `logout`, `me` |
| `security/service.go`, `dto.go` | login and token flow |
| `security/users.go` | `UserStore` interface and its default |
| `security/password.go` | PBKDF2 hashing with upgrade |
| `security/tokens.go`, `refresh.go` | access and refresh tokens |
| `security/*_test.go` | tests for every rule and check |
| `security/securitytest/` | test helpers that act as a user |

The login pieces start from the `g auth` templates, so the two stay alike
and fixes land in both. `.env` gets a commented security block (section 10).

## 2. Deny by default (zero-setup baseline)

The guard is registered in `dygo.AppGuards`, so it runs on every HTTP route
before the controller's own guards. A route with no matching rule is denied
with 401 if there is no token, so a new route is never open by accident.

Public by default, because a locked health check breaks every deploy:
`/health/live`, `/health/ready`, `/auth/login`, `/auth/register`,
`/auth/refresh`, and `/docs` plus `/openapi.json` outside production. These
are ordinary lines at the top of `rules.go`, so you can remove them.

At startup the app logs one line per rule and a count of routes per rule,
and `dygo routes` gets an `ACCESS` column (`public`, `authenticated`,
`role:admin`), so you can see what is protected without reading code.

## 3. Rules (SecurityFilterChain)

`rules.go` is the file you edit. Rules are checked top to bottom, and the
first match wins:

```go
func Rules() []Rule {
    return []Rule{
        Path("/health/**").PermitAll(),
        Path("/auth/login", "/auth/register", "/auth/refresh").PermitAll(),
        Path("/admin/**").HasRole("admin"),
        Method("GET").Path("/products/**").PermitAll(),
        Method("POST", "PUT", "DELETE").Path("/products/**").HasAuthority("products:write"),
        AnyRequest().Authenticated(),
    }
}
```

Checks: `PermitAll()`, `Authenticated()`, `HasRole(...)`, `HasAnyRole(...)`,
`HasAuthority(...)`, `HasAnyAuthority(...)`, `DenyAll()`, and
`Access(func(Principal, *http.Request) bool)` for anything else.

Patterns use `*` for one path segment and `**` for any number of segments.

Security details:

- Rules match the **route pattern** (`RouteInfo.Path`, such as
  `/orders/{id}`), not the raw URL. Tricks like `/admin/../x`, `//admin` or
  `%2F` cannot slip past a rule, because routing has already cleaned the path.
- Each route's rule is found once and cached, so a request does a map lookup,
  not a pattern scan. That matches the "no work on the hot path" design rule.
- At boot, a rule that matches no route is logged as a warning (usually a
  typo).
- A rule list that does not end in `AnyRequest()` gets `AnyRequest().Authenticated()`
  added, so the default cannot be lost.

## 4. Route-level and method-level authorization

Route options, for when a rule belongs next to the handler (like
`@PreAuthorize` on a controller method). They are checked after the rules
and can only make access stricter. `Public()` is the exception: it opens a
route.

```go
r.Get("/", dygo.HandleNoInput(c.List), security.Public())
r.Delete("/{id}", dygo.Handle(c.Delete), security.Roles("admin"))
r.Put("/{id}", dygo.Handle(c.Update), security.Authorities("orders:write"))
```

Inside services, for checks that need data (Go has no annotations, so these
are plain calls that the compiler checks):

```go
func (s *Orders) Cancel(ctx context.Context, id int) error {
    if err := security.Require(ctx, security.HasAuthority("orders:cancel")); err != nil {
        return err // 401 or 403
    }
    o, err := s.repo.Get(ctx, id)
    if err != nil {
        return err
    }
    // like @PostAuthorize("returnObject.owner == authentication.name")
    if err := security.RequireOwner(ctx, o.UserID); err != nil {
        return err // 403, unless the user is admin
    }
    ...
}
```

`RequireOwner` lets admins through (configurable). Both work in any code
path that has the request ctx, including queue jobs started from a request,
because the principal travels in ctx.

## 5. Principal, roles and authorities

```go
type Principal struct {
    Subject     string   // user ID, or the "sub" claim from the provider
    Email       string
    Roles       []string // "admin", "user"
    Authorities []string // "orders:write"
    Claims      map[string]any // every claim, for OIDC tokens
}

p, ok := security.CurrentUser(ctx)
```

Role hierarchy, in `roles.go`:

```go
var Hierarchy = RoleHierarchy{
    "admin": {"manager"},
    "manager": {"user"},
}
```

An admin passes `HasRole("user")`. Roles can also grant authorities
(`"manager": {"orders:write", "orders:cancel"}`), so tokens stay small and
the mapping lives in code.

## 6. Authentication (stateless bearer tokens)

**Own tokens (default).** Generated in `security/`, starting from the `g auth` code:

- **Access token:** HS256 JWT, 15 minutes by default (`AUTH_TOKEN_TTL`).
  Verification also checks `iss`, `aud` and `nbf`, and allows 30 seconds of
  clock skew.
- **Refresh token:** a random opaque token (not a JWT), stored as a SHA-256
  hash, valid for 30 days. `POST /auth/refresh` gives a new access token and a
  new refresh token (rotation). Reusing an old refresh token revokes the whole
  family, which catches stolen tokens.
- **Logout:** `POST /auth/logout` revokes the refresh token. Access tokens are
  short-lived, so they are not tracked (no per-request store lookup).
- **Passwords:** PBKDF2-SHA256, already in `g auth`. A hash made with fewer
  iterations than the current setting is upgraded on the next login, like
  `DelegatingPasswordEncoder.upgradeEncoding`. Login answers in the same time
  for an unknown email and for a wrong password, so attackers cannot find
  which emails exist.
- **Login rate limiting:** 10 calls a minute per client IP on register,
  login and refresh (429 after), with `ratelimit.Guard` on those routes.
- **Storage:** users and refresh tokens follow `DATABASE_URL`, like
  generated CRUD services (D68): memory for `memory://`, Postgres or MySQL
  otherwise, with a migration from `dygo g migration`.

**OAuth2 Resource Server (OIDC).** Set `OAUTH_ISSUER_URL` (Keycloak, Auth0,
Google, Entra ID):

- At boot the app reads `<issuer>/.well-known/openid-configuration`, then
  loads the JWKS. Boot fails with a clear message if the issuer cannot be
  reached in production. Outside production it logs a warning and retries.
- Tokens are checked for RS256 or ES256 signature, `iss`, `aud`
  (`OAUTH_AUDIENCE`), `exp` and `nbf`. `alg: none` and HS256 are always
  refused in this mode, which closes the classic key-confusion attack.
- Keys are cached and refreshed every hour. A token with an unknown `kid`
  makes the app fetch the keys again right away, at most once a minute,
  which handles key rotation without making it an easy DoS.
- Roles come from a configurable claim (`OAUTH_ROLES_CLAIM`, default
  `roles`; Keycloak users set `realm_access.roles`), and authorities from
  `scope` / `scp`.
- In this mode the login, register and refresh routes are not registered.
  The provider handles login.

All of this uses `crypto/rsa`, `crypto/ecdsa` and `net/http`. No third-party
JWT library.

## 7. Coming from `g auth`

An app uses `g auth` or `g security`, not both. When `g security` finds
`auth/`, it stops and prints the steps, which the guide repeats:

1. Delete `auth/` (users kept in memory are lost anyway; with a database the
   `users` table is reused as is).
2. Run `dygo g security`.
3. Remove `auth.Required()` (routes are locked by default), replace
   `auth.Roles` with `security.Roles` and `auth.UserFrom` with
   `security.CurrentUser`. `go build` points at every line to change.

## 8. 401 and 403 answers

- **401** (no token, bad token, expired token) carries
  `WWW-Authenticate: Bearer error="invalid_token"`, per RFC 6750. The message
  says "missing bearer token" or "invalid token", and never says why the
  check failed (expired vs bad signature goes to the debug log only).
- **403** says "access denied" and never lists the roles the route needs.
- Both use the existing `errs` JSON body, so clients see one error format.
- A failed check is logged at info level with the request ID, route and
  subject, never with the token. This is the audit trail, like
  `AuthorizationDeniedEvent`.

## 9. OWASP defenses for an API

| Defense | How |
|---|---|
| Security headers | `middleware.SecureHeaders` on by default: nosniff, `X-Frame-Options: DENY` (clickjacking), HSTS over HTTPS, `Referrer-Policy`, `Cache-Control: no-store` on authenticated responses |
| CORS | `middleware.CORS` from `CORS_ALLOWED_ORIGINS`. Empty means same origin only. `*` together with credentials is refused at boot |
| Brute force | login and refresh rate limits (section 6) |
| Token in URL | tokens are read only from the `Authorization` header, never from the query string |
| Body size | auth routes cap the body at 1 MB |
| CSRF, session fixation | not needed: no cookies or sessions. The doc explains why, and that cookie login would need `middleware.CSRF` |

## 10. Configuration (`.env`)

```sh
# --- security ---
# Signs access tokens; at least 32 characters (openssl rand -hex 32). Required in production.
AUTH_SECRET=
# Access token lifetime, and refresh token lifetime
AUTH_TOKEN_TTL=15m
AUTH_REFRESH_TTL=720h
# Use an external identity provider instead of the built-in login (Keycloak, Auth0, ...)
# OAUTH_ISSUER_URL=https://auth.example.com/realms/shop
# OAUTH_AUDIENCE=shop-api
# OAUTH_ROLES_CLAIM=realm_access.roles
# Origins allowed to call the API from a browser, comma separated
# CORS_ALLOWED_ORIGINS=https://shop.example.com
```

In production (`APP_ENV=production`) boot fails if `AUTH_SECRET` is missing
or short, if `/docs` is public, or if CORS allows `*` with credentials.

## 11. Testing

`securitytest` helpers, so tests don't need real logins:

```go
srv := dygotest.New(app.Module).CompileHTTP(t)
srv.GET("/orders").Do(t).Status(t, 401)
srv.GET("/orders").With(securitytest.User("ada", "user")).Do(t).Status(t, 200)
srv.DELETE("/orders/1").With(securitytest.User("ada", "user")).Do(t).Status(t, 403)
srv.DELETE("/orders/1").With(securitytest.Admin()).Do(t).Status(t, 204)
```

This needs a small `dygotest` addition: `Req.With(func(*http.Request))`.
The helpers sign a real token with the test secret, so the guard is tested
for real. For service tests, `securitytest.Context(ctx, principal)` gives a
ctx with a user.

Tests generated into the app cover: deny by default, each rule kind, the
role hierarchy, `Require` and `RequireOwner`, refresh rotation and reuse
detection, logout, the password upgrade, the rate limit, and OIDC against a
local fake issuer (`httptest` with a generated RSA key).

The CLI gets golden files for `g security` (flat and modular), and a test
that runs `dygo new` + `g security` + `go test` on the result, as for `g auth`.

## 12. Other generators in a security project

`dygo g controller`, `g service`, `g crud`, `g resource` and `g test` work as
today in both layouts (flat files in a simple project, `src/modules/<name>/`
in a modular one). When the app has a security module (`dygo.json` gets
`"security": true`), they change in three small ways:

- Generated routes are locked by the global rules like any other route. No
  `Public()` is added; the generator prints one line saying which rule covers
  the new routes.
- `g crud` and `g resource` pass `-write-role admin` by default: list and get
  need a signed in user, create, update and delete get
  `security.Roles("admin")`. `-public-read` makes list and get public.
- Generated tests sign in with `securitytest.User(...)` and also check the 401
  and 403 cases, so they pass without edits.

How the CLI knows: `g security` (or `dygo new -security`) writes
`"security": true` to `dygo.json`. Without `dygo.json`, the CLI looks for a
`security` module imported in the root module. `-public` on `g controller`
or `g crud` makes all of its routes `security.Public()`. Services are
unchanged: security sits on routes, and `security.Require` is added by hand.

## 13. Not in this phase

| Feature | Why not now |
|---|---|
| OAuth2 Client (login with Google, calling other APIs) | an API rarely needs it; later if asked |
| Authorization Server | a product of its own; use Keycloak or Auth0 |
| PAR | belongs to the authorization server |
| DPoP | rare in practice; a later add-on to the resource server |
| API keys | next after this phase, small (`Path(...).APIKey()`) |
| Sessions, form login, remember-me, CSRF | server-rendered app features, out of scope |
| ws and micro transports | the ws handshake is HTTP, so the guard covers it; message handlers inside micro, queue and events are trusted internal traffic. Documented, not enforced |
| MFA, password reset, email verification | app features; recipes later |

## 14. Milestones

Each milestone is one PR, with tests, golden files and docs.

1. **Core security layer.** `dygo g security` and `dygo new -security`, the
   `security/` package with register, login, me and access tokens (24h until
   milestone 2 adds refresh tokens), `rules.go`, the global guard, deny by
   default, `Principal`/`CurrentUser`, role hierarchy, route options,
   `Require`/`RequireOwner`, JSON 401/403, headers and CORS, the
   `securitytest` helpers and `dygotest` `Req.With`.
2. **Generators in a security project** (section 12). `dygo.json`
   `"security": true`, the fallback check, `-public`, `-public-read`,
   `-write-role`, generated tests that sign in, and the `dygo routes` ACCESS
   column.
3. **Token lifecycle.** Refresh tokens with rotation and reuse detection,
   logout, 15 minute access tokens, password rehash, login rate limit, users
   and tokens following `DATABASE_URL`.
4. **OAuth2 Resource Server.** `g security -oidc`, OIDC discovery, JWKS
   cache, RS256/ES256, claim mapping, a fake issuer for tests.
5. **Docs and example.** `docs/recipes/security.md` with the `g auth` switch, `docs/guide-cli.md`, and
   `examples/academy` moved to `g security`.

Milestone 1 alone is already useful: a locked-down API with login and the
app's own access tokens.

## 15. Decisions so far

| # | Decision | By |
|---|---|---|
| D69 | Two generators: `g auth` stays small, `g security` is the full layer | Dylan, 2026-11-02 |
| D70 | API scope (stateless resource server), not server-rendered apps | Dylan, 2026-11-02 |
| D71 | One `security/` package with login included; an app uses `g auth` or `g security`, not both | Dylan, 2026-11-02 |
| D72 | Rules match route patterns and the method, first match wins, cached per route | built in milestone 1 |
| D73 | Generators detect security from `dygo.json` (fallback: a package made by `g security`) and generate protected routes and signed-in tests | built in milestone 2 |
| D74 | Opaque refresh tokens stored hashed, rotated on use, a reused token revokes its login family; rate limit by IP only | built in milestone 3 |
| D75 | `g security -oidc` writes its own file set: a Verifier for the provider's tokens and no login code; `securitytest` runs a fake provider | built in milestone 4 |
| D76 | Cache checked tokens until they expire; spend refresh tokens with a conditional update | performance pass |

## 16. Questions for Dylan

Answered 2026-11-02: Dylan said go, which takes the recommendation on each.

1. **Public health and docs by default** (section 2). Recommended: yes, as
   editable lines in `rules.go`.
2. **Access token lifetime: 15 minutes** once refresh tokens exist
   (milestone 3), 24h before that. `g auth` keeps 24h.
3. **`RequireOwner` lets admins through** by default. Recommended: yes.
4. **`dygo new shop -security`** (section 1). Recommended: yes, it's small.
5. **`g crud` write routes need `admin` by default** (section 12).
   Recommended: yes; `-write-role` and `-public-read` change it.
