# Guide: the dygo CLI

The `dygo` CLI creates projects and generates code. It uses only the standard library and is part of the main module.

```sh
go install github.com/dygoframework/dygo/cmd/dygo@latest
dygo                    # one line per command
dygo help generate      # a command's arguments and flags (or: dygo g -h)
```

## New project

```sh
dygo new shop                        # one folder per module, see the layout below
dygo new shop -security             # secure from the start: runs dygo g security
dygo new shop -security -oidc       # the same, with login at Keycloak, Auth0, ... (dygo g security -oidc)
dygo new shop -db postgres           # with a database (or -db mysql): see "Database" below
cd shop && go run .                  # http://localhost:9900, docs at /docs
```

Apps listen on port 9900 by default (`PORT` in `.env`). Every project gets a `.env` with your local settings, ready to run, and a `.env.example` with the same settings to commit. Every setting has a comment saying what it does and which values it takes. `.env` is git ignored, so after a fresh clone run `cp .env.example .env`.

A project is split by module. The two files that start the app sit at the top, and everything else is a folder:

```
shop/
  main.go                 load config, create the app, listen
  app.module.go           AppModule (the root module) and AppOptions
  app_test.go             e2e tests: wiring check and a boot of the whole app
  src/
    config/               typed configuration (env and .env); a global module
    database/             the connection, a global module; *sql.DB shaped placeholder (a real pool with -db)
    health/               /health/live and /health/ready, pinging the database
    common/               types shared by features (Pagination)
    modules/              feature modules: users/, orders/, ... (dygo g resource adds them)
  dygo.json               tells dygo where features go
  Dockerfile  .env  .env.example  README.md
```

`main.go` and `app.module.go` are both package `main`, so the root module is one file you can open, not a package. The other packages sit in `src/`. Unlike `internal/`, Go does not stop other modules from importing `src/` packages; for an application nobody imports, that makes no difference. While dygo is unpublished, `-replace ../dygo-framework` points the project at a local checkout.

### dygo.json

```json
{"layout": "modular", "main": ".", "featuresDir": "src/modules", "rootModuleDir": "."}
```

`layout` is always `modular`. `main` is the default package for `routes`, `graph`, `check`, `info` and `start` at the project root. `featuresDir` is where `dygo g` puts a feature when run from the root, and `rootModuleDir` is where it is registered (in the module named `app`) when no closer module exists.

## Generate code

| Command | Creates | Registers |
|---|---|---|
| `dygo g module auth` (`mo`) | `src/modules/auth/module.go` from the root, `auth/module.go` elsewhere | `auth.Module` in the parent directory's module `Imports`, or `AppModule` in `app.module.go` |
| `dygo g service auth` (`s`) | `auth/service.go`, `service_test.go` | `dygo.Provide(NewService)` in the nearest module's `Providers` |
| `dygo g controller auth` (`co`) | `auth/controller.go`, `controller_test.go` | `dygo.Provide(NewController)` in the nearest module's `Controllers` |
| `dygo g resource order-items` (`res`) | `orderitems/`: module, service (stored in the database set by `DATABASE_URL`, memory by default), controller (CRUD), DTOs with `Validate`, an HTTP test | `orderitems.Module` in the parent module |
| `dygo g entity Product -fields "..."` (`en`) | `products/products.go`: the entity, create and update bodies with `Validate`, `Entity` and `Apply`. Run again on an existing entity, it adds the fields | Nothing |
| `dygo g crud Product` | For an existing entity: module, service, controller (list, create, get, update, delete) and HTTP test | `products.Module` in the parent module |
| `dygo g resource Product -fields "..."` (`res`) | Entity and CRUD in one step (`g entity` then `g crud`) | `products.Module` in the parent module |
| `dygo g command seed` (`cmd`) | `seed/command.go` (`Name`, `Description`, `Run`), `command_test.go` | `dygo.Provide(seed.NewCommand)` in the nearest module's `Providers` |
| `dygo g test products` | `products/routes_test.go`, one subtest per route the app registers for the package | Nothing |
| `dygo g auth` | `auth/`: register, login, me, password hashing, tokens, guard, tests | `auth.Module` in the root module, plus `AUTH_SECRET` in `.env.example` |
| `dygo g security` (`sec`) | `security/`: rules, global guard, roles, authorities, `Require`, register, login, me, tokens, CORS, tests and `securitytest` | `security.Module` in the root module, `security.CORS()` in `dygo.WithMiddleware`, settings in `.env` and `.env.example`, `"security": true` in `dygo.json` |
| `dygo g security -oidc` | the same, for tokens from an identity provider: `security/verifier.go` instead of users, passwords and login, and `GET /auth/me` | the same; `OAUTH_ISSUER_URL`, `OAUTH_AUDIENCE`, `OAUTH_ROLES_CLAIM` in `.env` |

"Nearest module" is the `&dygo.Module{...}` literal in the target directory or the closest one above it, inside the Go module. When a directory has several, the one named like the package or `"app"` wins. The edit inserts one line and adds the import, so your comments and layout stay. Nothing is overwritten; `-dry-run` prints the plan.

Names: `order-items` gives package `orderitems`, path `/order-items`, entity `OrderItem`. Types inside a package are not prefixed (`orderitems.Service`, not `OrderItemsService`).

## Entities and fields

```
$ dygo g entity Product

 Your entity Product already exists, so let's add some new fields!
 Current fields: id, title, price

 New field name (press <return> to stop adding fields):
 > releasedAt
 Field type (enter ? to see all types) [time]:
 >
 Can this field be empty (optional) (yes/no) [no]:
 > yes
 added: releasedAt time (optional)

 Add another field? Enter the field name (or press <return> to stop adding fields):
 >
UPDATE products/products.go (+ releasedAt)
UPDATE products/controller_test.go (sample values)
```

Name the entity in the singular or the plural, in any case. `Product`, `Products`, `product` and `products` all give the `products` package (`/products`) and the `Product` type.

In a terminal, leaving out `-fields` starts this loop. It asks for the name, the type (guessed from the name: `...At` is a time, `is...` a bool, `price` a float, and `?` lists every type) and whether the field is optional. Pressing Enter on an empty name stops the loop. It works the same for a new entity or resource. For scripts and CI, pass the fields as a flag:

```sh
dygo g resource Product -fields "title:string price:float stock:int notes:text? owner:email?"
dygo g entity Product -fields "sku:string"      # later: add a field to the existing entity
dygo g resource Product                          # in a terminal, without -fields: asks field by field
```

Types: `string`, `text`, `email`, `int`, `int64`, `float`, `bool`, `time`. Add `?` for an optional field. Required text fields must not be empty, `email` fields must be an address, and required `time` fields must be set. Validation runs in the validation pipe and answers 400 with every problem at once. Field names become Go names (`published-at` gives `PublishedAt`) and camelCase JSON names (`publishedAt`).

Every line that depends on the fields is in one file, `products/products.go`: the `Product` struct, `CreateProduct` with `Validate` and `Entity(id)`, and `UpdateProduct` (pointer fields, so a PATCH changes only what was sent) with `Validate` and `Apply`. The service calls `Entity` and `Apply`, so adding a field never touches the service or controller. `dygo g entity` on an existing entity inserts the new fields into each of those places, and adds sample values to the `CreateProduct{...}` bodies in the package's tests. Everything else in the files stays as you wrote it, and if you removed one of the methods, the CLI says which parts it could not update.

## CRUD for an entity

Make the entity first, then the CRUD around it:

```
$ dygo g entity Product            # the field loop above
$ dygo g crud Product               # or just dygo g crud, which asks for the entity

CREATE src/modules/products/module.go
CREATE src/modules/products/service.go
CREATE src/modules/products/controller.go
CREATE src/modules/products/controller_test.go
UPDATE app.module.go (Imports += products.Module)

 Success! CRUD for Product:

   GET    /products          List    (index)
   POST   /products          Create  (new)
   GET    /products/{id}     Get     (show)
   PATCH  /products/{id}     Update  (edit)
   DELETE /products/{id}     Delete  (delete)
```

`dygo g crud` takes the entity's type (`Product`) or its package (`products`). It reads the entity file, and the test fills every field with a valid sample value (email fields get an address). It writes:

| File | What it holds |
|---|---|
| `controller.go` | `List`, `Create`, `Get`, `Update`, `Delete` (JSON, so no HTML views) |
| the entity file | `CreateProduct` and `UpdateProduct`, with `Validate` (400 with every error) |
| `service.go` | storage on `*sql.Repository[Product]`; `DATABASE_URL` in `.env` picks memory, Postgres or MySQL |
| `controller_test.go` | the whole create, list, update, delete flow |

No CSRF token is needed for a token-authenticated JSON API; protect routes with `auth.Required()`.

`dygo g resource Product -fields "..."` still does both steps at once.

## Console commands

```sh
dygo g command seed      # seed/command.go, registered in the nearest module
dygo run seed --users 10 # builds the app and runs the command inside its context
dygo run                 # lists the commands with their descriptions
```

A command is a provider with `Name()` and `Run(ctx, args)` (and optionally `Description()`), so it can inject any service. Package `console` builds the application context, runs the command, then shuts down, so `OnModuleInit` and shutdown hooks run as they do for the server. `ctx` is cancelled on Ctrl+C. New projects' `main.go` starts with:

```go
if len(os.Args) > 1 { // go run . <command>
    os.Exit(console.Main(ctx, AppModule(cfg, log), os.Args[1:], dygo.WithLogger(log)))
}
```

In a project made before this existed, `dygo g command` prints that snippet.

## Route tests

`dygo g test products` runs the app (as `dygo routes` does) and writes `products/routes_test.go` with one subtest per route of the package. GET routes without path parameters are checked for their status. The others are skipped with a TODO and a request ready to fill in (body and real IDs). The test boots the package's `Module` with `dygotest`.

## Authentication

```sh
dygo g auth
```

This writes `auth/` (in `src/modules/` for a modular project) and imports it in the root module:

| Route | Does |
|---|---|
| `POST /auth/register` | `{"email","password"}`, 201 with the user, 409 if the email is taken, 400 for a bad email or a password under 8 characters |
| `POST /auth/login` | 200 with `{"accessToken","tokenType":"Bearer","expiresAt"}`, 401 for a wrong email or password (same message) |
| `GET /auth/me` | The signed in user, 401 without a valid token |

Protect your own routes and read the user:

```go
r.Post("/", dygo.Handle(c.Create), auth.Required())
r.Delete("/{id}", dygo.Handle(c.Delete), auth.Roles("admin")) // 403 without the role
claims, _ := auth.UserFrom(ctx)                                // claims.UserID(), claims.Roles
```

Once a module's routes use `auth.Required()`, that module's own tests must boot auth as well, or the build fails with `*auth.Guard is not visible`. Use `dygotest.New(&dygo.Module{Name: "app", Imports: []*dygo.Module{auth.Module, Module}})` and send a token from `POST /auth/login`.

Passwords are hashed with PBKDF2-HMAC-SHA256 and tokens are HS256 JWTs, both from the standard library. Set `AUTH_SECRET` (32 or more characters, for example `openssl rand -hex 32`) in `.env`. In production it is required. In development a random secret is used, so tokens stop working on restart. Users are kept in memory, so replace `Users` in `users.go` with your database. The module is global, so any module can use `auth.Required()` without importing it.

## Security

```sh
dygo g security
```

The [security recipe](recipes/security.md) covers all of this in depth. `dygo g auth` gives you login and lets you protect routes one by one. `dygo g security` is the deny-by-default version: one `security/` package (in `src/modules/` for a modular project) that includes the same login routes, and a global guard that locks **every** route unless a rule opens it. An app uses one of the two, not both: `dygo g security` stops and prints the steps to switch when it finds a `g auth` package.

Who may call what is one ordered list in `security/rules.go`. The first matching rule wins, and the guard always ends the list with `AnyRequest().Authenticated()`:

```go
func Rules(cfg *Config) []Rule {
	rules := []Rule{
		Path("/").PermitAll(),
		Path("/health/**").PermitAll(),
		Path("/auth/register", "/auth/login", "/auth/refresh", "/auth/logout").PermitAll(),
		Path("/admin/**").HasRole("admin"),
		Method("POST", "PUT", "PATCH", "DELETE").Path("/products/**").HasAuthority("products:write"),
	}
	if !cfg.Production() {
		rules = append(rules, Path("/docs", "/openapi.json").PermitAll())
	}
	return append(rules, AnyRequest().Authenticated())
}
```

It also turns on `BearerAuth` in the app's `openapi.Options`, so `/docs` has an **Authorize** button: paste the access token from `POST /auth/login` there to try protected routes.

Patterns match the route pattern (`/orders/{id}`), not the raw URL, so path tricks cannot get past a rule; `*` is one segment and `**` any number. Next to a handler, and inside services:

```go
r.Get("/catalog", dygo.HandleNoInput(c.Catalog), security.Public())    // open, whatever the rules say
r.Delete("/{id}", dygo.Handle(c.Delete), security.Roles("admin"))       // like @PreAuthorize
r.Put("/{id}", dygo.Handle(c.Update), security.Authorities("orders:write"))

err := security.Require(ctx, security.HasAuthority("orders:cancel")) // 401 or 403, just return it
err = security.RequireOwner(ctx, order.UserID)                        // like @PostAuthorize; admins pass
p, _ := security.CurrentUser(ctx)                                     // p.Subject, p.Roles, p.Authorities
```

`security/roles.go` holds the role hierarchy (`"admin": {"user"}` lets an admin pass `HasRole("user")`) and the authorities each role grants. Without a token the answer is 401 with `WWW-Authenticate: Bearer`; without access, 403 `access denied`, which never says which role was missing. Settings are in `.env`: `AUTH_SECRET` (required in production; `dygo g security` writes a random one into `.env`, and `openssl rand -hex 32` makes another), `AUTH_TOKEN_TTL` (15m), `AUTH_REFRESH_TTL` (720h) and `CORS_ALLOWED_ORIGINS` for browsers.

Login returns a short access token and a refresh token. `POST /auth/refresh` with `{"refreshToken": "..."}` returns a new pair and spends the old refresh token; sending a spent one again ends that login, which catches a stolen token. `POST /auth/logout` ends the login (its access tokens run out within `AUTH_TOKEN_TTL`). Register, login and refresh allow 10 calls a minute per client IP. Users and refresh tokens are kept where `DATABASE_URL` says: in memory for `memory://`, else in the `users`, `refresh_tokens` and `refresh_families` tables, which `dygo g migration` writes (add a unique index on `users.email`).

In tests, sign in without a real login:

```go
securitytest.Setup(t) // before building the app
srv := dygotest.New(AppModule()).CompileHTTP(t)
srv.GET("/orders").Do(t).Status(t, 401)
srv.GET("/orders").With(securitytest.User("7", "user")).Do(t).Status(t, 200)
srv.DELETE("/orders/1").With(securitytest.Admin()).Do(t).Status(t, 204)
```

### Login at an identity provider (`-oidc`)

When users sign in at Keycloak, Auth0, Okta, Entra ID or Google, the API only checks their tokens, as an OAuth2 resource server:

```sh
dygo g security -oidc
```

```sh
OAUTH_ISSUER_URL=https://auth.example.com/realms/shop   # the provider's issuer
OAUTH_AUDIENCE=shop-api                                 # tokens must name this API in "aud"
OAUTH_ROLES_CLAIM=realm_access.roles                    # Keycloak; the default is roles
```

At startup the app reads `<issuer>/.well-known/openid-configuration` and the provider's keys. It accepts RS256 and ES256 tokens from that issuer for that audience, and refuses `alg: none` and HS256. Roles come from `OAUTH_ROLES_CLAIM`, the token's scopes become authorities (`security.Authorities("orders:write")`), and `p.Claims` has every claim. There are no register, login, refresh or logout routes: the provider does that. `GET /auth/me` shows what the token says. Rules, `Roles`, `Require` and the generators work the same as without `-oidc`. In tests, `securitytest.Setup(t)` starts a fake provider, so `securitytest.User` and `Admin` work unchanged.

In production the issuer must use https and `OAUTH_AUDIENCE` must be set, or the app does not start. Without `OAUTH_ISSUER_URL` in development it starts and refuses every token.

### Generators in a security project

`dygo g security` sets `"security": true` in `dygo.json`, and from then on the generators write secure code without any flag:

| Command | Routes | Generated test |
|---|---|---|
| `dygo g resource products` / `dygo g crud Product` | list and get need a signed in user; create, update and delete get `security.Roles("admin")` | boots `security.Module` too, checks 401 without a token and 403 for a plain user, then runs the CRUD signed in |
| `... -public-read` | list and get get `security.Public()` | checks that reads work without a token |
| `... -write-role accountant` | writes need `accountant` instead (`-write-role -` for any signed in user) | signs writes in as that role |
| `dygo g controller billing` | needs a signed in user (the rules decide) | checks 401, then 200 signed in |
| `... -public` | `security.Public()` on every route | checks 200 without a token |

Each command prints one line saying who may call the new routes. Services do not change: security sits on routes, and `security.Require` in a service is yours to add. Without `dygo.json`, a package made by `dygo g security` in the root or `src/modules` counts too.

The [Security recipe](recipes/security.md) explains the generated package, refresh tokens and login at an identity provider such as Keycloak (`-oidc`).

## Background work and transports

```sh
dygo g listener notify      # events listener: notify/listener.go, Events(r) with events.On
dygo g cron reports         # scheduled jobs: reports/jobs.go, an hourly s.Cron
dygo g job welcome-email    # queue processor: JobName, Payload, Process(r) with queue.Handle
dygo g gateway chat         # WebSocket gateway: chat.ping handler, OnConnection, OnDisconnect
dygo g rpc users-rpc        # micro handlers: users-rpc.get with micro.Handle
```

Each writes the provider and a test, and registers `dygo.Provide(New...)` in the nearest module. When the root module does not import the package's module yet (`events.Module()`, `schedule.Module()`, `queue.Module(...)`, `ws.Module(...)`), the CLI says which one to add. A gateway needs `go get github.com/dygoframework/dygo/ws`. See the [transports guide](guide-transports.md).

## Database

`dygo db` sets up MySQL or Postgres and runs migrations. Migrations are the source of truth for the
schema: there is no schema sync from structs.

```sh
dygo new shop -db postgres           # a new project with a database (-db mysql too)
dygo db init                         # or add one to an existing project (asks for the settings)
docker compose up -d                 # dygo new -db and dygo db init -compose write compose.yaml
dygo db create                       # CREATE DATABASE shop
dygo g migration                     # migrations/0001_update_schema.up.sql and .down.sql,
                                     # with the SQL for every entity that changed
dygo db migrate                      # apply what is pending
dygo db status                       # applied and pending migrations
```

| Command | What it does |
|---|---|
| `dygo new shop -db postgres\|mysql` | `dygo new` plus everything `dygo db init -compose` does |
| `dygo db init [-driver postgres\|mysql] [-url URL \| -host -port -user -password -name] [-compose] [-force]` | writes `DATABASE_URL` to `.env` and `.env.example` (with the password `change-me`), imports `postgres.Module()` or `mysql.Module()` (in a modular project it replaces the `src/database` placeholder), adds `migrations/embed.go`, and wires the `db:*` commands into `main`. It asks in a terminal for what the flags leave out, and refuses to replace a different `DATABASE_URL` without `-force`. |
| `dygo g migration [name]` (`mig`) | the next numbered `.up.sql` and `.down.sql` pair, filled for every entity whose table differs (see below); without a name it is called `update_schema`, and nothing is written when nothing changed |
| `dygo db create [--if-not-exists]` | creates the database named in `DATABASE_URL` |
| `dygo db drop --force [--if-exists]` | drops it |
| `dygo db migrate [--to N] [--dry-run] [--out-of-order]` | applies pending migrations; `--dry-run` prints their SQL |
| `dygo db rollback [--steps N \| --to N]` | runs the down files, newest first |
| `dygo db status [--check]` | lists migrations; `--check` exits 1 when some are pending (CI, deploy scripts) |
| `dygo db schema create` | applies every migration to an empty database |
| `dygo db schema drop --force [--all]` | rolls back every migration; `--all` drops every table instead |

`drop`, `rollback` and `schema drop` refuse to run when `APP_ENV=production`
unless `--allow-production` is given.

### Migrations from an entity

`dygo g migration` compares every entity
with its table and fills both files with the difference, whatever the
migration is called (the name is only a label). Without a name it is called
`update_schema`, and when every table already matches it writes nothing and
says "No changes". For each entity:

- If no earlier migration creates the table, `CREATE TABLE` with a column
  for each field and `DROP TABLE` in the down file.
- Otherwise `ALTER TABLE ... ADD COLUMN` for each new field (with a default,
  so existing rows get a value) and `DROP COLUMN` for each column that is no
  longer a field (with a comment, since it deletes data); the down file
  reverses them.

Column names follow `sql.Repository`: the `db` tag or the snake_case field
name, with `id` as the primary key unless a field has `db:",pk"`. The table is
the one given to `sql.ForFeature[Product]("...")`, or the plural snake_case
entity name. Types come from the Go types (`string` is `TEXT` on Postgres and
`VARCHAR(255)` on MySQL, `float64` is `DOUBLE PRECISION` or `DOUBLE`,
`time.Time` is `TIMESTAMPTZ` or `DATETIME(6)`, pointers and `sql.Null*` are
nullable), for the database in `DATABASE_URL`.

**Renamed fields.** A renamed field looks like a dropped column and a new
one. In a terminal, `dygo g migration` asks for each dropped column that has a
new column of the same kind ("Did you rename products.note to description
(TEXT)? [y/N]"); a yes writes `RENAME COLUMN`, which keeps the data. Outside
a terminal, or to skip the question, name them:
`dygo g migration rename_products_note -rename note:description` (comma
separated for several; field names such as `PublishedAt` work too). A drop
that looks like a rename gets a comment with the `-rename` to use.

**Changed types.** A column changes when its field moves to another kind of
type (text, integer, float, bool, time, bytes, json): `ALTER COLUMN ... TYPE
... USING col::type` on Postgres, `MODIFY COLUMN` on MySQL. Changes inside a kind (`INT` to
`BIGINT`, `TEXT` to `VARCHAR(255)`) are left alone, so hand-written types
stay. A field that becomes a pointer or `sql.Null*` drops `NOT NULL`; one
that stops being nullable gets `NOT NULL`, after an `UPDATE` that fills the
`NULL`s with the zero value. The down file reverses each change.

**Data loss warnings.** Every type change says what it does to the rows
already in the table, as a comment above the statement and as a `WARNING:`
line in the output:

```sql
-- price changes from DOUBLE PRECISION to BIGINT.
-- DATA LOSS: decimals are rounded (2.7 becomes 3); the down migration cannot bring them back. Back up the table first.
-- MAY FAIL on numbers too big for BIGINT. Postgres then rolls the migration back.
ALTER TABLE products ALTER COLUMN price TYPE BIGINT USING price::bigint;
```

`DATA LOSS` marks values that change for good: decimals rounded (float to
integer), large integers rounded (integer to `REAL` or `DOUBLE`), numbers
merged into `true` (integer to `BOOLEAN` on Postgres), text cut to a
`VARCHAR(n)` length (Postgres), bytes turned into hex text (`BYTEA` to
`TEXT`). `MAY FAIL` marks values that do not convert, such as text that is
not a number or a date. Postgres rolls a failed migration back; MySQL cannot
roll back a schema change, so check the data first there. The down file gets
the same comment for the reverse change. A change where every value converts
(integer to text, `BOOLEAN` to integer) says so.

**Safe mode for rolling deploys (`-safe`).** A plain `RENAME COLUMN` or
`DROP COLUMN` breaks the instances still running the old code during a
deploy. `-safe` splits the change into expand and contract
migrations:

```sh
dygo g migration rename_note_in_products -safe -rename note:description
dygo db migrate             # 1. expand: adds description, copies note into it, syncs both
                            # 2. deploy the new code everywhere
dygo db migrate --contract  # 3. contract: stops the sync, drops note
```

- The expand migration adds the new column, copies the data, and installs a
  trigger that copies every write between the old and the new column (a
  PL/pgSQL function on Postgres, two one-statement `BEFORE` triggers on
  MySQL). Old code writing `note` and new code writing `description` then
  see the same data. New fields and `NULL` changes go here too.
- The contract migration is the next version. It starts with
  `-- dygo:after-deploy`, so `dygo db migrate` (and `migrate.OnBoot`) stop
  before it and print what is waiting, and `status` shows it as "pending,
  after deploy". `status --check` does not fail on it. Run it with
  `dygo db migrate --contract` (`./app db:migrate --contract` in production)
  once no old code runs. It drops the trigger, the old column and the
  columns that are no longer fields. `dygo db schema create` applies it
  directly, since an empty database has no old code.
- A type change under the same column name is refused, since old and new code
  would read one column as two types. Give the field a new column
  (`db:"price_cents"`) and run with `-rename price:price_cents`: the trigger
  casts between the two types.
- Both down files work: rolling back the contract brings the old column and
  the trigger back, and rolling back the expand removes them.

On MySQL with binary logging, creating triggers needs the `TRIGGER`
privilege and, for users without `SUPER`, `log_bin_trust_function_creators=1`.

This compares the entity with what the earlier migration files build (their
`CREATE`, `ALTER`, `RENAME` and `DROP TABLE` statements, including `ALTER
COLUMN` and `MODIFY`), not with a live database, so it needs no connection and
works the same in CI. It is a starting point to review. `dygo g entity`
prints the command to run after it adds an entity or fields, once the
project has migrations.

**How it runs.** The CLI has no database driver (it uses only the standard
library), so the app runs the commands itself. `dygo db init` adds this to
`main`, before the app is created:

```go
if code, ok := migrate.Main(ctx, postgres.Dialect(), migrations.FS, os.Args[1:]); ok {
	os.Exit(code)
}
```

`dygo db migrate` builds the main package and runs it as `app db:migrate` in
the project root, so the app's driver, migrations and `.env` are used. In
production run the binary itself, for example as a deploy step:
`./shop db:migrate`. `migrate.Main` reads `DATABASE_URL` on its own and opens
one connection, without creating the app, so it works before the database
exists and does not need the app's other services.

**Migration files.** `NNNN_name.up.sql` and `NNNN_name.down.sql` in
`migrations/`, embedded in the binary by `migrations/embed.go`. A
`dygo_migrations` table records what ran, and a database lock
(`pg_advisory_lock`, MySQL `GET_LOCK`) makes a second instance wait. Each file
runs in a transaction with its `dygo_migrations` row; Postgres rolls back a
failed migration completely, while MySQL commits each DDL statement on its own,
so keep MySQL migrations to one change each. Start a file with
`-- dygo:no-transaction` for statements that refuse a transaction, such as
`CREATE INDEX CONCURRENTLY`. A pending migration older than the newest applied
one (two branches each added one) stops `migrate` until you renumber it or pass
`--out-of-order`. For a small single instance service, `migrate.OnBoot(migrations.FS)`
applies migrations while the app boots instead.

Not built yet: `dygo db seed`, `db reset`, `db validate`, `db sql`, `db shell`,
`dygo g crud --db` (waits for the `sqltest` helpers), and a schema diff
(`db diff`, `db schema update`), which is planned for a later release.

## AI models

```sh
dygo ai init -provider anthropic     # or openai, or ollama for a local model (asks in a terminal)
```

`dygo ai init` adds the `ai` module to the app it runs in; apps that never run
it do not download it. It:

- writes `AI_PROVIDER`, `AI_MODEL` (and `AI_BASE_URL` for ollama or
  `-base-url`) to `.env` and `.env.example`, with an empty `AI_API_KEY` in
  `.env` and `change-me` in `.env.example`. Put your key in `.env`, which is
  git-ignored; dygo never takes it as a flag, so it stays out of your shell
  history;
- imports `ai.Module(openai.Provider, anthropic.Provider)` in the root module,
  so switching vendors later is a change to `.env`;
- makes the app test boot with a fake model
  (`.OverrideValue(dygo.TypeOf[ai.Model](), fake.New())`), so `go test` needs
  no key and never calls the vendor;
- adds `github.com/dygoframework/dygo/ai` to `go.mod` and runs `go mod tidy`.

| Flag | |
|---|---|
| `-provider openai\|anthropic\|ollama` | the vendor; ollama is the openai provider at `http://localhost:11434/v1`, with no key |
| `-model id` | the model (default: one per provider) |
| `-base-url URL` | a compatible server; https, or http on localhost |
| `-force` | replace AI settings already in `.env` (it refuses otherwise) |
| `-skip-tidy` | do not run `go mod tidy` |

Running it again changes nothing and keeps your key. The app then refuses to
start until `AI_API_KEY` is set, with `anthropic: AI_API_KEY is required`. The [AI guide](guide-ai.md) shows how to use the model.

## Look at a running app's shape

```sh
dygo routes          # METHOD PATH HANDLER INPUT OUTPUT, sorted by path; -json for tools
dygo graph           # modules with imports, exports, providers and what each injects
dygo graph -format mermaid > graph.mmd    # or dot, or json
dygo check           # wiring errors, exit status 1; nothing is built
dygo info            # Go, CLI and framework versions, module and provider counts
```

Each takes an optional main package (default `.`). They run it with `DYGO_INSPECT_OUT` set; `dygo.Create` then writes its graph (and, for `routes`, its routes) to that file and returns `dygo.ErrInspected` instead of starting. `check`, `graph` and `info` build nothing. `routes` builds the providers, because controllers register routes from their instances, but runs no startup hook and opens no port. Whatever `main` does before `dygo.Create`, such as loading config, still has to succeed.

For a unit test or CI without the CLI:

```go
func TestWiring(t *testing.T) {
    if _, err := dygo.Describe(AppModule(&Config{}, slog.New(slog.DiscardHandler))); err != nil {
        t.Fatal(err)
    }
}
```

## Development server

```sh
dygo start -watch              # rebuild and restart on .go, go.mod, go.sum and .env changes
dygo start -watch ./cmd/api -- -flag value
```

The new build replaces the running app only when it compiles, so a typo leaves the last good version serving. The old app gets SIGTERM, so shutdown hooks run (killed after 10 s). Tests, hidden directories, `vendor` and `testdata` are not watched.

## Route directives (dygo gen)

Instead of writing `Routes`, mark the controller and its methods:

```go
//dygo:controller /notes
//dygo:use auth.Roles("user")
//dygo:import "example.com/shop/auth"
type Controller struct{ svc *Service }

//dygo:get /
func (c *Controller) List(ctx context.Context) ([]Note, error) { ... }

//dygo:get /{id}
//dygo:use openapi.Summary("Get a note")
func (c *Controller) Get(ctx context.Context, in IDIn) (Note, error) { ... }
```

`dygo gen` writes `dygo_gen.go` with the `Routes` method, choosing `dygo.Handle`, `HandleNoInput` or `HandleRaw` from the parameters. Directives: `controller <prefix>`, `get|post|put|patch|delete|head|options|all <path>`, `use <route option expression>` (on the type for every route, on a method for that route), and `import "<path>"` for packages that only the directives use (dygo, openapi, meta, validate, interceptors, ratelimit, errs, net/http and time are known). Add `//go:generate dygo gen` to a file, and run `dygo gen -check` in CI to catch a stale file. A type with directives must not also have a hand written `Routes`.
