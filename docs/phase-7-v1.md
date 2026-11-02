# Phase 7: v1.0

**Goal:** a stable, documented framework you can recommend to others.

## Tasks
1. **API review:** every exported name in the core, check naming and doc comments, remove anything experimental or move it to `x/`.
2. **API freeze:** tag `v1.0.0`; from then on follow semver and keep a CHANGELOG.
3. **Docs site:** getting started, the guides from `docs/guide-*.md`, and a recipes section (auth, SQL, testing).
4. **Performance:** publish benchmarks against bare `net/http`, chi, Gin, and Huma, with the methodology.
5. **Dogfooding:** build one real service on Dygo (for example a Dygo Academy backend) and fix what hurts.
6. **Security:** `govulncheck` in CI, a SECURITY.md, fuzz tests for the binder.

## Exit criteria
- A real service runs in production on Dygo for at least a few weeks.
- No breaking API change needed for a month before tagging.

## Status (2026-10-31)

| Task | State |
|---|---|
| API review | Done: report in the project files at `phase7/api-review.md`. Doc fixes applied. Breaking changes applied with Dylan's OK: `WithRouteMiddleware` renamed to `RouteMiddleware`; `InspectReport` and `RouteSummary` unexported (the `Graph*` types stay, since `Describe` returns them). |
| API freeze | Not started: needs a git repo and a month without breaking changes. `CHANGELOG.md` is ready. |
| Docs site | Done: `mkdocs.yml`, `make docs` (strict build passes), [getting started](getting-started.md), recipes for [auth](recipes/auth.md), [SQL](recipes/sql.md), [testing](recipes/testing.md). Publishing waits for the repo. |
| Performance | Done: `benchmarks/` module and its README with method and results. |
| Dogfooding | First pass done with `examples/academy` (findings below). The exit criterion, a real service in production for a few weeks, is still open. |
| Security | Done: `SECURITY.md`, `make vulncheck`, binder fuzz targets, CI job. After the move to Go 1.27 (D52) govulncheck is clean on every module. |

Also added: `.github/workflows/ci.yml`, `.golangci.yml`, tests for `errs`,
`meta`, `validate`, `interceptors`.

## Dogfooding findings

Found while building `examples/academy` and writing the recipes.

Fixed:

1. A panic in a lifecycle hook crashed the process and skipped the rest of
   shutdown. Hooks now recover into a `*dygo.PanicError` (`lifecycle.go`,
   `TestPanickingHooksAreErrors`).
2. Generated `Delete` doc comment grammar ("removes a Enrollment").

Open, in rough order of how much they hurt:

1. **Testing a feature module that uses a global module is awkward.** An
   override can only replace a token that something provides, and every
   singleton is built even when nothing uses it, so a test must import the
   global module and override its config and connection too (see the SQL
   recipe). Proposal: `dygotest.Builder.OverrideModule(old, replacement)`.
2. **Generated CRUD tests break as soon as routes use `auth.Required()` or
   `auth.Roles()`.** The example adds `auth.BearerFor(app, roles...)`.
   Proposal: `dygo g auth` writes that helper, and `dygo g resource` uses it
   when the project has an auth module.
3. **No way to create the first admin with `dygo g auth`.** The example adds
   `AdminSeed` (`AUTH_ADMIN_EMAIL`, `AUTH_ADMIN_PASSWORD`). Proposal: put it in
   the template.
4. **`-fields courseId:int` produces the JSON name `courseID`.** JSON
   clients expect `courseId`. Proposal: JSON tags keep the spelling the user
   typed; only the Go name gets the initialism.
5. **`dygo g listener` and `dygo g job` print "Import events.Module() in the
   root module" instead of doing it.** The CLI already edits the root module
   for features, so it could add these imports too.
6. **The generated auth module reads its config with `config.ForFeature` from
   `.env` and the real environment,** so the `config.Load(map)` that tests use
   does not reach it; tests have to use `t.Setenv`.
7. **Auth tests take about 6.5 s** because of the PBKDF2 cost. Proposal: a
   lower cost when `APP_ENV=test`.
8. **About 0.5 µs and 5 allocations of fixed cost per request** (benchmarks).
   Irrelevant next to a database call, but the first target for performance
   work.
9. `dygo routes` does not show which routes are guarded or which roles they
   need; a column for it would make reviewing access rules easier.
