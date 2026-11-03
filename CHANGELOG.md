# Changelog

All notable changes to Dygo are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and from v1.0.0 the
project follows [semantic versioning](https://semver.org/). What the semver
promise covers is in the "Stability" section of the package docs
(`go doc github.com/dygoframework/dygo`).

Sub-modules (`ws`, `graphql`, `micro/*`, `queue/asynq`, `observe`,
`validate/playground`, `sql`) are versioned and tagged on their own, such as
`ws/v1.0.0-beta.1`. Their changes are listed here too, under the module's name.

## Unreleased

### Removed

- The simple `dygo new` layout. Every project now gets the modular layout
  (`main.go`, `app.module.go`, `src/config`, `src/database`, `src/health`,
  `src/modules`), and `dygo g` no longer writes flat files such as
  `products.service.go` into package `main`. In a project made with the simple
  layout, `dygo g` now creates a package per feature, as it does elsewhere.
  The `-t`/`-template` flag is gone.

### Changed

- sql: a query on a table or column the database does not have now fails
  with `sql.ErrSchema` ("schema out of date"), and its message says to run
  `dygo g migration` and `dygo db migrate`. Postgres 42P01/42703 and MySQL
  1146/1054 map to it. It still answers 500.
- `dygo g security` (and `dygo new -security`) sets `BearerAuth: true` in the
  app's `openapi.Options`, so `/docs` has an Authorize button for the access
  token.
- `dygo g security` writes a random `AUTH_SECRET` into `.env` (not
  `.env.example`), so tokens survive restarts from the start. The
  "AUTH_SECRET is required in production" and "at least 32 characters" errors
  of `g security` and `g auth` now say to run `openssl rand -hex 32`.

### Fixed

- Running `dygo db init` a second time in a new project no longer adds a
  second driver module to `app.module.go` when `src/database` already opens
  that driver.

## v1.0.0-beta.1 (2026-11-03)

### Added

- First public beta, v1.0.0-beta.1, published from
  github.com/dygoframework/dygo. Sub-modules are tagged with their path, such
  as `sql/v1.0.0-beta.1`.
- `dygotest.Integration`, `RequireEnv` and `RequireCommand` mark a test as an
  integration test: it skips under `go test -short`, and when its database,
  environment variable or program is missing.
- Testing layout (docs/guide-testing.md): `make test-unit`,
  `make test-integration` with `compose.test.yml`, micro benchmarks in
  `bench_test.go` files across the modules (cache, graphql, micro, micro/grpc,
  micro/kafka, ws, durable providers), and `make bench`, `bench-save`,
  `bench-diff`. CI runs unit tests, every test against Postgres, MySQL and
  Redis, and each benchmark once.

### Changed

- README rewritten: security by default, a short example, and the benchmark
  summary; the build status and plan notes are gone.
- **Breaking:** the module path is now `github.com/dygoframework/dygo` (it was
  `github.com/dygoacademy/dygo`), and the same for every sub-module. Update
  imports and `go.mod` files.

- **Breaking:** `WithRouteMiddleware` is now `RouteMiddleware`, so every
  route option is a bare name and only app options start with `With`.
- **Breaking:** `InspectReport` and `RouteSummary` are no longer exported.
  They are the file format between `dygo.Create` and the CLI, which keeps its
  own copy. `Describe`, `GraphInfo` and `ErrInspected` stay public.
- Go 1.27 is now the minimum for every module, and third party
  dependencies are upgraded to their latest versions, which clears the
  govulncheck findings in `observe`, `micro/grpc` and `examples/http-users`.

### Added

- OWASP Top 10 checks in CI: an `owasp` job runs gosec, govulncheck,
  the security tests and fuzzing (`make owasp`); `docs/owasp.md` maps each
  category to its checks. Actions are pinned to commits and Dependabot keeps
  modules and actions current. `dygo g security` now logs failed logins and
  adds `middleware.SecureHeaders` when the app has none, and its generated
  tests check forged own tokens, security logging and the key refetch limit.

- `dygo g security` and `dygo new -security`: a deny-by-default security package for APIs. Every route needs a bearer token unless
  the ordered rules in `security/rules.go` or `security.Public()` open it;
  roles with a hierarchy, authorities, `security.Require` and
  `RequireOwner` for checks in services, register, login and me routes,
  JSON 401 and 403, CORS from `CORS_ALLOWED_ORIGINS`, and `securitytest`
  helpers that sign in as a user in tests.
- In a security project (`"security": true` in `dygo.json`),
  `dygo g resource`, `g crud` and `g controller` write protected routes
  (CRUD writes need `admin`) and tests that check 401 and 403 and sign in
  with `securitytest`. New flags: `-public`, `-public-read`, `-write-role`.
- Security recipe (`docs/recipes/security.md`), and `examples/academy` moved from
  `dygo g auth` to `dygo g security`.

- Faster `dygo g security`: checked tokens are cached until they
  expire (a request with an OIDC token went from 52 µs to 2 µs, with an own
  token from 6.2 µs to 1.7 µs); refresh tokens are spent with one
  conditional update, without a global lock, which also stops two app
  instances from accepting the same refresh token; provider keys are
  fetched without blocking requests.
- `dygo g security -oidc` and `dygo new -security -oidc`: the API
  checks tokens from an identity provider (Keycloak, Auth0, ...) set by
  `OAUTH_ISSUER_URL`: OIDC discovery, JWKS with key rotation, RS256 and
  ES256, `iss`, `aud`, `exp` and `nbf`, roles from `OAUTH_ROLES_CLAIM` and
  scopes as authorities. `securitytest` runs a fake provider in tests.
- Token lifecycle in `dygo g security`: login returns a refresh token;
  `POST /auth/refresh` rotates it and a reused token ends the login;
  `POST /auth/logout`; access tokens last 15 minutes (`AUTH_TOKEN_TTL`,
  `AUTH_REFRESH_TTL`); passwords hashed with fewer iterations are rehashed
  on login; register, login and refresh are rate limited per IP; users and
  refresh tokens follow `DATABASE_URL`.
- `dygo g migration` also writes tables for structs given to
  `sql.NewRepository[E]` in the file that declares them.
- `dygotest`: `Req.With(func(*http.Request))` changes a request before it is
  sent, for helpers such as `securitytest.User`.
- Services made by `dygo g crud` and `dygo g resource` store their data in
  the database set by `DATABASE_URL`: `memory://` keeps it in memory,
  a Postgres or MySQL URL in that database, with no code change. New apps
  import `sql.Module(nil)`, which `dygo db init` turns into the driver's
  module.
- `sql`: `DATABASE_URL=memory://` opens an in-memory database that
  `Repository` works on (`sql.Memory`, `sql.MemoryModule`, `sql.MemoryURL`,
  `sql.ErrMemory`, `DB.IsMemory`). `Module(nil)` opens it without a driver,
  and `migrate.Main` says there is nothing to migrate.
- `dygo g migration` compares every entity with its table: the
  name is optional and only a label, and with no
  name and no changes it writes nothing.
- `dygo new` writes `DATABASE_URL=memory://` to `.env` and `.env.example`
  for every template, with a comment that the data is kept in memory until
  `dygo db init` puts a Postgres or MySQL URL there.
- `sql`: the pool logs `database connected` with the URL (password
  redacted) at startup, so the app shows which database it uses, and the
  module reads `.env.local` before `.env`, like the generated app's config.
- Generated in-memory services (`dygo g crud`, `resource`, `auth`) log
  `<name> stored in memory` at startup and say in their comment that the
  data is lost when the app stops; `dygo db init`
  writes `DATABASE_URL` with example formats; docs/recipes/sql.md has
  "From memory to a database".
- `sql/migrate`: numbered `.up.sql`/`.down.sql`
  migrations from an `embed.FS`, a `dygo_migrations` table, a database lock,
  `Migrator` (`Up`, `Down`, `DownTo`, `Status`, `Pending`), `OnBoot`, and
  `Main` for the app's `db:*` commands.
- `dygo db`: `init` writes `DATABASE_URL` to `.env` and wires the
  driver and migrations into the app; `create`, `drop`, `migrate`,
  `rollback`, `status`, `schema create` and `schema drop` run in the app.
  `dygo new -db postgres|mysql` and `dygo g migration` too.
- `dygo g migration` fills the files from the entity the name is about:
  `CREATE TABLE`/`DROP TABLE` for a new table, `ALTER TABLE ADD/DROP COLUMN`
  when the entity's fields changed since the earlier migrations, for Postgres
  or MySQL. Renamed fields become `RENAME COLUMN` (asked in a terminal, or
  `-rename old:new`), and a field that changes its kind of type or
  nullability becomes `ALTER COLUMN TYPE`/`SET NOT NULL` (Postgres) or
  `MODIFY COLUMN` (MySQL). `dygo g entity` prints the migration command to
  run next.
- `dygo g migration` warns about data loss on type changes: a
  `DATA LOSS` or `MAY FAIL` comment above each conversion, in the up and down
  files, and `WARNING:` lines in the output.
- `dygo g migration -safe` writes an expand migration and a contract
  migration for rolling deploys, with a trigger that keeps a renamed column
  and its old name in sync. `sql/migrate`: up files marked
  `-- dygo:after-deploy` wait for `db:migrate --contract` (`Contract`,
  `Migrator.Held`, `Migration.AfterDeploy`, `State.AfterDeploy`).

- `sql` module: `postgres.Module()` and
  `mysql.Module()` read `DATABASE_URL` and the pool settings from the
  environment, ping the database at boot, close the pool after the HTTP drain
  and add a readiness indicator. `DB.MapError` turns driver errors into
  `ErrNotFound` (404), `ErrDuplicate` and `ErrForeignKey` (409),
  `ErrConstraint` (422) and `ErrRetryable`. Passwords never appear in errors.
  `sql.Prefix` and `sql.Named` add a second database.
- `sql`: `DB.Get`, `Select`, `Exec` and `Query` with `?`
  placeholders on both databases (rewritten to `$1` for Postgres, skipping
  strings and comments; `??` is a literal `?`), `sql.In` for `IN (?)`, rows
  scanned into structs by `db` tag or snake_case field name. `DB.Tx` and
  `TxWith` carry the transaction in `ctx` with savepoints for nested
  calls, retries on serialization failures and deadlocks, rollback on panic,
  `sql.InTx`, `sql.Detach` and `ErrTxDone`; `DB.Transactional()` runs a
  route in one transaction. Aliases for `NullString`, `Null[T]` and the
  isolation levels.
- `sql`: `sql.Repository[T]` and `sql.ForFeature[T](table)`:
  `Find`, `FindBy`, `List`, `Page`, `Count`, `Exists`, `Create`,
  `CreateMany`, `Update`, `Patch` with `sql.Set`, `Delete`; clauses `Where`,
  `OrderBy` (column names checked, `ErrUnknownColumn` answers 400), `Limit`
  and `Offset`; `db:"id,pk,auto"`, `created` and `updated` tag options;
  `sql.Using(name)` for a named pool. `Dialect` gains `Quote` and
  `Returning`. The MySQL dialect now adds `clientFoundRows=true` to the DSN.
- `docs/getting-started.md` and recipes for
  authentication, SQL databases and testing; an MkDocs site config
  (`mkdocs.yml`, `make docs`).
- `benchmarks/` module comparing Dygo with net/http, chi, Gin and Huma, with
  the method and results.
- `SECURITY.md`, `make vulncheck` (govulncheck on every module) and fuzz
  targets for the request binder (`make fuzz`).
- CI workflow (`.github/workflows/ci.yml`) and the `.golangci.yml` that
  `make lint` uses.
- Package docs: "Writing a transport" and "Stability" sections.
- Tests for `errs`, `meta`, `validate` and `interceptors`.
- `examples/academy`, a course platform backend built with the CLI to
  show the framework in a real app (auth with roles, events, a background queue).

### Fixed

- `dygo g security` in a project without `dygo/sql` now adds
  `sql.Module(nil)` and the module requirement, so the app builds.
- A panic in a lifecycle hook (`OnModuleInit`, `OnApplicationShutdown` and
  the others) crashed the process and skipped the remaining shutdown hooks. It
  is now returned as a `*dygo.PanicError`, and the other hooks still run.
- `dygo g resource`: the generated `Delete` doc comment read "removes a
  Enrollment".

## Included in the first beta

- Core: container, modules, providers, tokens, groups, `Lazy`, `Optional`,
  parameter structs, lifecycle hooks, and boot errors that list every problem.
- HTTP: controllers, typed handlers, the struct tag binder, versioning, global
  prefix, body limits, JSON error bodies (`errs`) and request scope.
- Pipeline: guards, interceptors, pipes and exception filters, typed metadata
  (`meta`) and validation (`validate`, `validate/playground`).
- Production: `config`, `logger`, `middleware` (request ID, logging, secure
  headers, CORS, CSRF, compression), `health`, `openapi`, `ratelimit` and the
  `observe` module for OpenTelemetry.
- The `dygo` CLI: `new`, `g` generators, `db`, `routes`, `graph`, `check`,
  `info`, `start -watch` and `gen`.
- Transports and background work: `events`, `schedule`, `cqrs`, `cache`,
  `queue` (memory and `queue/asynq`), `micro` (NATS, gRPC, Redis, Kafka),
  `ws` and `graphql`.
- `dygotest` for unit and HTTP tests.
