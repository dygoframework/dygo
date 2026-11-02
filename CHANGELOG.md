# Changelog

All notable changes to Dygo are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and from v1.0.0 the
project follows [semantic versioning](https://semver.org/). What the semver
promise covers is in the "Stability" section of the package docs
(`go doc github.com/dygoframework/dygo`).

Sub-modules (`ws`, `graphql`, `micro/*`, `queue/asynq`, `observe`,
`validate/playground`, `sql`) are versioned and tagged on their own, such as
`ws/v0.9.0`. Their changes are listed here too, under the module's name.

## Unreleased

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

- **Breaking:** the module path is now `github.com/dygoframework/dygo` (it was
  `github.com/dygoacademy/dygo`), and the same for every sub-module. Update
  imports and `go.mod` files.

- **Breaking:** `WithRouteMiddleware` is now `RouteMiddleware`, so every
  route option is a bare name and only app options start with `With`.
- **Breaking:** `InspectReport` and `RouteSummary` are no longer exported.
  They are the file format between `dygo.Create` and the CLI, which keeps its
  own copy. `Describe`, `GraphInfo` and `ErrInspected` stay public.
- Go 1.27 is now the minimum for every module (D52), and third party
  dependencies are upgraded to their latest versions, which clears the
  govulncheck findings in `observe`, `micro/grpc` and `examples/http-users`.

### Added

- OWASP Top 10 checks in CI (D77): an `owasp` job runs gosec, govulncheck,
  the security tests and fuzzing (`make owasp`); `docs/owasp.md` maps each
  category to its checks. Actions are pinned to commits and Dependabot keeps
  modules and actions current. `dygo g security` now logs failed logins and
  adds `middleware.SecureHeaders` when the app has none, and its generated
  tests check forged own tokens, security logging and the key refetch limit.

- `dygo g security` and `dygo new -security` (phase 9, D69 to D72): a
  deny-by-default security package for APIs. Every route needs a bearer token unless
  the ordered rules in `security/rules.go` or `security.Public()` open it;
  roles with a hierarchy, authorities, `security.Require` and
  `RequireOwner` for checks in services, register, login and me routes,
  JSON 401 and 403, CORS from `CORS_ALLOWED_ORIGINS`, and `securitytest`
  helpers that sign in as a user in tests.
- In a security project (`"security": true` in `dygo.json`, D73),
  `dygo g resource`, `g crud` and `g controller` write protected routes
  (CRUD writes need `admin`) and tests that check 401 and 403 and sign in
  with `securitytest`. New flags: `-public`, `-public-read`, `-write-role`.
- Security recipe (`docs/recipes/security.md`), and `examples/academy` moved from
  `dygo g auth` to `dygo g security`.

- Faster `dygo g security` (D76): checked tokens are cached until they
  expire (a request with an OIDC token went from 52 µs to 2 µs, with an own
  token from 6.2 µs to 1.7 µs); refresh tokens are spent with one
  conditional update, without a global lock, which also stops two app
  instances from accepting the same refresh token; provider keys are
  fetched without blocking requests.
- `dygo g security -oidc` and `dygo new -security -oidc` (D75): the API
  checks tokens from an identity provider (Keycloak, Auth0, ...) set by
  `OAUTH_ISSUER_URL`: OIDC discovery, JWKS with key rotation, RS256 and
  ES256, `iss`, `aud`, `exp` and `nbf`, roles from `OAUTH_ROLES_CLAIM` and
  scopes as authorities. `securitytest` runs a fake provider in tests.
- Token lifecycle in `dygo g security` (D74): login returns a refresh token;
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
  the database set by `DATABASE_URL` (D68): `memory://` keeps it in memory,
  a Postgres or MySQL URL in that database, with no code change. New apps
  import `sql.Module(nil)`, which `dygo db init` turns into the driver's
  module.
- `sql`: `DATABASE_URL=memory://` opens an in-memory database that
  `Repository` works on (`sql.Memory`, `sql.MemoryModule`, `sql.MemoryURL`,
  `sql.ErrMemory`, `DB.IsMemory`). `Module(nil)` opens it without a driver,
  and `migrate.Main` says there is nothing to migrate.
- `dygo g migration` compares every entity with its table (D67): the
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
- `sql/migrate` (phase 8, milestone 5): numbered `.up.sql`/`.down.sql`
  migrations from an `embed.FS`, a `dygo_migrations` table, a database lock,
  `Migrator` (`Up`, `Down`, `DownTo`, `Status`, `Pending`), `OnBoot`, and
  `Main` for the app's `db:*` commands.
- `dygo db` (D58): `init` writes `DATABASE_URL` to `.env` and wires the
  driver and migrations into the app; `create`, `drop`, `migrate`,
  `rollback`, `status`, `schema create` and `schema drop` run in the app.
  `dygo new -db postgres|mysql` and `dygo g migration` too.
- `dygo g migration` fills the files from the entity the name is about (D59):
  `CREATE TABLE`/`DROP TABLE` for a new table, `ALTER TABLE ADD/DROP COLUMN`
  when the entity's fields changed since the earlier migrations, for Postgres
  or MySQL. Renamed fields become `RENAME COLUMN` (asked in a terminal, or
  `-rename old:new`), and a field that changes its kind of type or
  nullability becomes `ALTER COLUMN TYPE`/`SET NOT NULL` (Postgres) or
  `MODIFY COLUMN` (MySQL). `dygo g entity` prints the migration command to
  run next.
- `dygo g migration` warns about data loss on type changes (D61): a
  `DATA LOSS` or `MAY FAIL` comment above each conversion, in the up and down
  files, and `WARNING:` lines in the output.
- `dygo g migration -safe` (D60) writes an expand migration and a contract
  migration for rolling deploys, with a trigger that keeps a renamed column
  and its old name in sync. `sql/migrate`: up files marked
  `-- dygo:after-deploy` wait for `db:migrate --contract` (`Contract`,
  `Migrator.Held`, `Migration.AfterDeploy`, `State.AfterDeploy`).

- `sql` module (phase 8, milestone 1): `postgres.Module()` and
  `mysql.Module()` read `DATABASE_URL` and the pool settings from the
  environment, ping the database at boot, close the pool after the HTTP drain
  and add a readiness indicator. `DB.MapError` turns driver errors into
  `ErrNotFound` (404), `ErrDuplicate` and `ErrForeignKey` (409),
  `ErrConstraint` (422) and `ErrRetryable`. Passwords never appear in errors.
  `sql.Prefix` and `sql.Named` add a second database.
- `sql` milestone 2: `DB.Get`, `Select`, `Exec` and `Query` with `?`
  placeholders on both databases (rewritten to `$1` for Postgres, skipping
  strings and comments; `??` is a literal `?`), `sql.In` for `IN (?)`, rows
  scanned into structs by `db` tag or snake_case field name. `DB.Tx` and
  `TxWith` carry the transaction in `ctx` (D53) with savepoints for nested
  calls, retries on serialization failures and deadlocks, rollback on panic,
  `sql.InTx`, `sql.Detach` and `ErrTxDone`; `DB.Transactional()` runs a
  route in one transaction. Aliases for `NullString`, `Null[T]` and the
  isolation levels.
- `sql` milestone 3: `sql.Repository[T]` and `sql.ForFeature[T](table)`
  (D57): `Find`, `FindBy`, `List`, `Page`, `Count`, `Exists`, `Create`,
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
  dogfood the framework (auth with roles, events, a background queue).

### Fixed

- `dygo g security` in a project without `dygo/sql` now adds
  `sql.Module(nil)` and the module requirement, so the app builds.
- A panic in a lifecycle hook (`OnModuleInit`, `OnApplicationShutdown` and
  the others) crashed the process and skipped the remaining shutdown hooks. It
  is now returned as a `*dygo.PanicError`, and the other hooks still run.
- `dygo g resource`: the generated `Delete` doc comment read "removes a
  Enrollment".

## Before v1.0

There were no tagged releases before v1.0.0. The framework was built in phases;
this is what each one added.

### Phase 6: transports and background work (2026-10-31)

- One enhancer pipeline for every transport (`NewEnhancers`, `Transport`,
  `Endpoint`), hybrid apps with `App.ConnectMicroservice` (D37 to D39).
- `Discover[T]` and `ModuleRef.App` for packages that find providers (D40).
- Packages `events`, `schedule` (own cron parser), `cqrs`, `cache`, `queue`
  with a memory driver, and `micro` with an in-process transport (D41 to D45).
- Modules `queue/asynq`, `micro/nats`, `micro/grpc`, `micro/redis`,
  `micro/kafka`, `ws` and `graphql` (D44 to D49).
- Durable providers: `Durable()`, `WithContextIDStrategy`, `ContextID` (D46).
- Request scoped WebSocket gateways with `ws.PerMessageScope()` and
  `App.WithRequestScope` (D51).
- `RawJSON`, a GraphQL query cache and APQ, encode-once WebSocket broadcasts
  (D50).
- `dygo g listener|cron|job|gateway|rpc`.

### Phase 5: the `dygo` CLI (2026-10-31)

- `dygo new` (simple and `-t modular` layouts), `dygo g` generators for
  modules, services, controllers, resources, entities, CRUD, commands, tests
  and auth (D32 to D35).
- `dygo routes`, `graph`, `check`, `info` and `start -watch`, which inspect an
  app by running it up to `dygo.Create` (D29).
- `dygo gen` for route directives (D30), package `console` (D34).

### Phase 4: production essentials (2026-10-30)

- Packages `config`, `logger`, `middleware` (request ID, logging, secure
  headers, CORS, CSRF, compression), `health`, `openapi`, `ratelimit` (GCRA,
  D28) and the `observe` module for OpenTelemetry (D24, D25).
- Route metadata on `RouteInfo` and `RoutesOf(ctx)` (D26).
- `examples/http-users` with Docker and Jaeger (D27).

### Phase 3: the pipeline (2026-10-30)

- Guards, interceptors, pipes and exception filters at global, controller and
  route level, with dependency injection (D17 to D20, D22).
- Typed metadata (`meta` package), validation (`validate` package and the
  `validate/playground` module, D21), `interceptors.Timeout` and `Logging`.

### Phase 2: the HTTP layer (2026-10-29)

- Controllers, typed handlers (`Handle`, `HandleNoInput`, `HandleRaw`), the
  struct tag binder, responders, versioning, global prefix, body limits
  (D10 to D13, D16).
- JSON error bodies in package `errs` (D14).
- Request scope with `Scoped[T]` (D15), `dygotest` HTTP helpers.

### Phases 0 and 1: the core (2026-10-29)

- The container, modules, providers, tokens, groups, `Lazy`, `Optional`,
  parameter structs, lifecycle hooks and boot errors that list every problem
  (D1 to D9).
- `dygotest` for tests.

