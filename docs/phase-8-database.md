# Phase 8: Database (`sql` module)

**Goal:** a ready-made database module for MySQL and Postgres, so an app gets a
configured connection pool, CRUD, custom queries, transactions, health checks,
metrics and test helpers without writing its own `database` package. It
stays close to `database/sql` and plain SQL instead of
becoming an ORM.

**Status (2026-10-31):** milestones 1 (connection), 2 (queries and
transactions) and 3 (`Repository[T]`) are done: `sql/`, `sql/postgres`,
`sql/mysql`, unit tests on a fake driver, a fuzzed placeholder rewriter, and a
shared integration suite (`sql/internal/suite`) that CI runs against Postgres
16 and 17 and MySQL 8.0 and 8.4. Decisions are D53 to D57. Milestones 1 to 3
are what `sql/v0.1.0-beta.1` ships. Next: milestone 4 (`sqltest`, hooks,
observe metrics).

Milestone 5 is partly done (D58): `sql/migrate` (runner, lock, `Main` for the
`db:*` commands, `OnBoot`), `dygo db init|create|drop|migrate|rollback|status|schema create|drop`,
`dygo new -db` and `dygo g migration`, tested in CI against the same databases.
`dygo g crud --db` waits for `sqltest` (milestone 4). The command set follows
the comparison Dylan approved on 2026-10-31; a schema diff from
entities (`db diff`, `db schema update`) is a later milestone (option C).

Not measured yet: the section 8 targets for `Repository.Find` and `Create`
(the benchmark lands with the `benchmarks/` update in milestone 6).

Not done yet from milestone 2: `events`, `queue` and `schedule` do not call
`sql.Detach` themselves, because the core cannot import `sql`. That needs a
small core hook (a registry of context detachers that those packages run);
until then an async listener started inside a transaction gets
`sql.ErrTxDone` and the fix is `sql.Detach(ctx)` at the call site.

## 1. Scope

In scope:

- MySQL (8.0 and newer) and Postgres (the two newest major versions), through
  `database/sql`.
- Connection pool configured from env, opened and pinged at boot, closed after
  the HTTP drain on shutdown.
- Health indicator for `/health/ready`.
- Custom queries: `Get`, `Select`, `Exec` with results scanned into structs.
- CRUD without SQL: a generic `Repository[T]` built from `db` struct tags.
- Transactions that every query and repository joins.
- Driver error mapping to `errs` (duplicate key is 409, missing row is 404).
- Query hooks for tracing and slow query logs, pool metrics for `observe`.
- Optional read replica.
- Test helpers: a transaction per test that is rolled back.
- Generators: `dygo new --db`, `dygo g crud --db`, migration files.

Out of scope (users can add these themselves on top):

- Relations, eager loading, joins generated from struct fields, and a query
  builder. Joins are written as SQL with `Select`.
- Schema sync from structs. Schema changes go through
  migrations.
- MariaDB, SQLite, SQL Server and Mongo. The `Dialect` interface leaves room for
  them later.
- GORM and ent adapters. They stay recipes; a `sql/gorm` package can come after
  v0.1 if users ask.

## 2. Layout and dependencies

```
sql/                    module github.com/dygoframework/dygo/sql (own go.mod)
  sql.go                DB, Config, Open, Module, ForFeature
  query.go              Get, Select, Exec, rebind
  scan.go               struct scan plans (built once per type)
  repo.go               Repository[T], options (Where, OrderBy, Limit, Offset)
  tx.go                 Tx, savepoints, retry
  dialect.go            Dialect interface
  errors.go             ErrNotFound, error mapping to errs
  hook.go               Hook interface (tracing, slow log)
  doc.go                package doc with examples
  postgres/             Dialect + Module for pgx (imports github.com/jackc/pgx/v5)
  mysql/                Dialect + Module for go-sql-driver/mysql
  sqltest/              test helpers
```

Rules, so the module can move to its own repository later without changes for
users other than the import path:

- `sql/` imports only Dygo's public API (`dygo`, `config`, `errs`, `health`,
  `dygotest`), never `internal/`.
- No package outside `sql/` imports it. The CLI only writes code that imports it.
- Its tests live inside `sql/`; it has its own CI job and tags (`sql/v0.x.y`).
- `sql` itself depends only on the standard library and the Dygo core. The
  drivers are dependencies of `sql/postgres` and `sql/mysql` only. Both are
  listed in the one go.mod, but a Postgres app only compiles and links pgx.
- `observe` does not import `sql` and `sql` does not import `observe`. The hook
  interface uses standard library types only, so `observe` can satisfy it
  structurally (section 7).

## 3. Public API

### 3.1 Connecting

```go
import (
    "github.com/dygoframework/dygo/sql"
    "github.com/dygoframework/dygo/sql/postgres" // or sql/mysql
)

var AppModule = &dygo.Module{
    Name: "app",
    Imports: []*dygo.Module{
        postgres.Module(),           // reads DATABASE_URL and DB_* from env
        users.Module,
    },
}
```

`postgres.Module(opts ...sql.Option)` returns a global module that provides
`*sql.DB` and a health indicator. Options:

| Option | Meaning |
|---|---|
| `sql.Prefix("REPORTS_")` | read `REPORTS_DATABASE_URL`, `REPORTS_DB_MAX_OPEN`, ... |
| `sql.Named("reports")` | provide under a named token instead of `*sql.DB`, for a second database |
| `sql.WithConfig(cfg)` | use a fixed `sql.Config` instead of env (tests, CLIs) |
| `sql.WithReplica(url)` | reads outside a transaction go to the replica |
| `sql.WithHook(h)` | add a query hook (section 7) |

`sql.Config` (filled by `config.Load`, so `.env` files and validation work as
everywhere else):

| Field | Env var | Default |
|---|---|---|
| `URL` | `DATABASE_URL` | required |
| `MaxOpen` | `DB_MAX_OPEN` | 25 |
| `MaxIdle` | `DB_MAX_IDLE` | 25 |
| `ConnMaxLifetime` | `DB_CONN_MAX_LIFETIME` | 30m (must stay below MySQL `wait_timeout`) |
| `ConnMaxIdleTime` | `DB_CONN_MAX_IDLE_TIME` | 5m |
| `ConnectTimeout` | `DB_CONNECT_TIMEOUT` | 5s, for the boot ping |
| `SlowQuery` | `DB_SLOW_QUERY` | 200ms, 0 turns the slow log off |
| `ReplicaURL` | `DATABASE_REPLICA_URL` | empty |

Boot: open, apply pool settings, ping with `ConnectTimeout`. A failure stops
`dygo.Create` with a `BootError` that names the module and hides the password.
Shutdown: `OnApplicationShutdown` closes the pool after the HTTP server has
drained.

### 3.2 Custom queries

```go
var u User
err := db.Get(ctx, &u, "SELECT id, email FROM users WHERE id = ?", id)   // sql.ErrNotFound if no row

var list []Stats
err = db.Select(ctx, &list, `SELECT user_id, COUNT(*) AS n FROM orders
    WHERE created_at > ? GROUP BY user_id ORDER BY n DESC LIMIT 10`, since)

res, err := db.Exec(ctx, "UPDATE users SET active = ? WHERE id = ?", false, id)

rows, err := db.Query(ctx, "SELECT ...", args...) // *database/sql.Rows, for streaming
```

- Placeholders are always `?`. The Postgres dialect rewrites them to `$1, $2`
  once per query string and caches the result. The rewrite skips `?` inside
  quotes, comments and dollar quoted strings; `??` is a literal `?` (for the
  Postgres JSON operators `?`, `?|`, `?&`).
- `Get` and `Select` scan by column name into `db` tags (falling back to the
  snake_case field name), into a struct, a scalar (`int64`, `string`, ...) or a
  slice of either. Unknown columns are an error, so a typo is caught by the
  first test.
- `sql.In(ids)` expands a slice into `?, ?, ?` for `WHERE id IN (?)`.

### 3.3 CRUD with `Repository[T]`

```go
type User struct {
    ID        int64     `db:"id,pk,auto"`        // primary key, set by the database
    Email     string    `db:"email"`
    Name      string    `db:"name"`
    CreatedAt time.Time `db:"created_at,created"` // set on insert
    UpdatedAt time.Time `db:"updated_at,updated"` // set on insert and update
    Note      string    `db:"-"`                  // not a column
}

// In the feature module:
var Module = &dygo.Module{
    Name:      "users",
    Providers: []dygo.Provider{sql.ForFeature[User]("users"), dygo.Provide(NewService)},
}

func NewService(users *sql.Repository[User]) *Service { ... }
```

| Method | SQL |
|---|---|
| `Find(ctx, id)` | `SELECT ... WHERE pk = ?`; `sql.ErrNotFound` (maps to 404) |
| `FindBy(ctx, sql.Where("email = ?", e))` | first matching row |
| `List(ctx, opts...)` | `Where`, `OrderBy("created_at DESC")`, `Limit`, `Offset` |
| `Page(ctx, page, size, opts...)` | `List` plus `COUNT(*)`, returns `sql.Page[T]{Items, Total, Page, Size}` |
| `Count(ctx, opts...)` | `SELECT COUNT(*)` |
| `Exists(ctx, opts...)` | `SELECT 1 ... LIMIT 1` |
| `Create(ctx, &u)` | `INSERT`; fills `auto` columns (Postgres `RETURNING`, MySQL `LastInsertId`) |
| `CreateMany(ctx, []T)` | one multi row `INSERT` |
| `Update(ctx, &u)` | `UPDATE ... WHERE pk = ?`, all columns; `ErrNotFound` if no row changed |
| `Patch(ctx, id, sql.Set{"name": n})` | update listed columns only; names checked against the tags |
| `Delete(ctx, id)` | `DELETE ... WHERE pk = ?` |

- The column list and SQL text for each method are built once when the
  repository is created, so a request does no reflection on the struct.
- `OrderBy` and `Set` accept only column names known from the tags, so user
  input in a sort parameter cannot inject SQL. `Where` takes a SQL fragment
  with `?` arguments; the guide warns never to build it with string
  concatenation.
- Anything the repository cannot express is a custom query on the same type:
  `repo.DB().Select(ctx, &out, "SELECT ... JOIN ...")`.

### 3.4 Transactions

```go
err := db.Tx(ctx, func(ctx context.Context) error {
    if err := orders.Create(ctx, &o); err != nil {
        return err // rolls back
    }
    return stock.Patch(ctx, o.ProductID, sql.Set{"qty": newQty})
}) // commits when fn returns nil
```

- Every `Get`, `Select`, `Exec` and repository call uses the transaction found
  in `ctx`, so a service can combine several repositories (decision 1).
- A nested `Tx` becomes a savepoint, so a helper that opens its own
  transaction still works inside a caller's.
- `sql.TxOptions{Isolation, ReadOnly, Retries}`: with `Retries > 0`, a
  serialization failure (Postgres 40001) or deadlock (MySQL 1213) reruns `fn`.
- A panic in `fn` rolls back and re-panics, so the request pipeline still turns
  it into a 500.
- Guardrails, because the transaction is not visible in signatures:
  - `sql.Detach(ctx)` returns a `ctx` without the transaction. `events` (async
    listeners), `queue` producers and `schedule` jobs should call it, so work
    started from inside a transaction never uses it after commit (needs a core
    hook, see the status note at the top).
  - Using a finished transaction returns `sql.ErrTxDone` with the query text,
    instead of the driver's generic error.
  - `sql.InTx(ctx)` reports whether `ctx` carries a transaction, for tests and
    for code that must run inside one.
  - The guide states the rule plainly: inside `db.Tx`, everything that gets
    that `ctx` is in the transaction, and a query with another `ctx` is not.
- Optional HTTP helper: `sql.Transactional()` route option runs the whole
  handler in one transaction, for apps that
  want it per route.

### 3.5 Errors

`sql/postgres` and `sql/mysql` map driver errors to `errs` so controllers need
no driver imports:

| Case | Postgres | MySQL | Result |
|---|---|---|---|
| no row | `sql.ErrNoRows` | `sql.ErrNoRows` | `sql.ErrNotFound`, 404 |
| unique violation | 23505 | 1062 | `sql.ErrDuplicate`, 409, `code: DUPLICATE`, constraint name in the log only |
| foreign key | 23503 | 1451, 1452 | `sql.ErrForeignKey`, 409 |
| not null or check | 23502, 23514 | 1048, 3819 | 422 |
| serialization or deadlock | 40001, 40P01 | 1213 | retried by `Tx` when asked, otherwise 500 |
| anything else | | | unchanged, 500, logged |

The mapped errors wrap the driver error, so `errors.As` to `*pgconn.PgError`
or `*mysql.MySQLError` still works.

## 4. Testing helpers (`sql/sqltest`)

```go
func TestCreateUser(t *testing.T) {
    db := sqltest.Open(t, postgres.Dialect())   // DYGO_TEST_POSTGRES_URL, skips when unset
    sqltest.Migrate(t, db, migrations.FS)       // optional
    root := &dygo.Module{Name: "test", Imports: []*dygo.Module{
        sqltest.Module(t, db),                  // one tx for the whole test, rolled back at t.Cleanup
        users.Module,
    }}
    srv := dygotest.New(root).CompileHTTP(t)
    srv.POST("/users").JSON(map[string]any{"email": "a@b.c"}).Do(t).Status(t, 201)
}
```

- `sqltest.Module(db)` replaces the pool with a single transaction, so tests
  can run in parallel against one database without cleaning tables.
- Controller tests that should not touch a database keep using a fake
  repository, as in the current SQL recipe.
- This also addresses dogfooding finding 1 (overriding a global database
  module in tests).

## 5. Migrations

Decided (decision 3): `sql/migrate`, a small built in runner instead
of a dependency.

- Files `NNNN_name.up.sql` and `NNNN_name.down.sql`, read from an `embed.FS`.
- A `dygo_migrations` table records what ran; a lock (Postgres advisory lock,
  MySQL `GET_LOCK`) stops two instances migrating at once.
- Run from the command line, not at boot by default: `dygo db migrate`
  (which runs `app db:migrate` through `migrate.Main`), `./app db:migrate`
  in production. `dygo g migration` writes the files.
- `migrate.OnBoot()` option for small single instance services.

## 6. CLI

| Command | What it writes |
|---|---|
| `dygo new shop --db postgres` (or `mysql`) | imports `postgres.Module()` instead of the in-memory placeholder `database` package, adds the DB to `compose.yaml` and `.env.example`, adds `migrations/` and the `migrate` command |
| `dygo g crud User --db` | entity with `db` tags, `sql.ForFeature[User]`, a service on `*sql.Repository[User]`, controller, tests with `sqltest`, and a `create_users` migration |
| `dygo g migration add_users_name` | the next numbered up and down files, filled with `CREATE TABLE` or `ALTER TABLE` from the entity the name is about (D59) |
| `dygo db init` | the same database setup as `dygo new --db`, for an existing project |
| `dygo db create\|drop\|migrate\|rollback\|status`, `dygo db schema create\|drop` | nothing: they run the app's `migrate.Main` (D58, docs/guide-cli.md "Database") |

Without `--db`, the generators keep today's output, so nothing changes for
existing projects.

## 7. Observability

```go
type Hook interface {
    Before(ctx context.Context, query string) context.Context
    After(ctx context.Context, query string, d time.Duration, err error)
}
```

- `sql` ships a slow query hook that logs the query text and duration through
  `slog`, never the arguments.
- `observe` gets `observe.SQLHook()` (a span per query, named by the first
  keyword and table) and `observe.SQLPool(name, statsFunc)`, which exports
  `database/sql.DBStats` (open, in use, idle, wait count, wait duration) as
  OpenTelemetry metrics. Both use standard library types only, so neither
  module imports the other.
- Queries on `db.Query` rows count until `rows.Close()`.

## 8. Performance targets

Measured in `benchmarks/` against hand written `database/sql` code on the same
driver (sqlmock style fake driver, so the database itself is not measured):

- `Get` and `Select` into a struct: under 10% more time and no more than 2
  extra allocations per row compared with hand written `rows.Scan`.
- `Repository.Find` and `Create`: same targets.
- Placeholder rewrite: zero allocations after the first call for a query.

## 9. Milestones

Each milestone ends with `go vet`, `go test -race`, lint and govulncheck green
on every module, docs updated, and a PR.

| # | Milestone | Content | Size |
|---|---|---|---|
| 1 | Connection | `sql` module, `Config`, `Open`, pool, boot ping, shutdown, health, `Dialect`, `postgres` and `mysql` packages, error mapping, password redaction | 2 to 3 days |
| 2 | Queries and transactions | `Get`, `Select`, `Exec`, `Query`, `In`, placeholder rewrite, scan plans, `Tx` with savepoints, retries, `Transactional()` | 3 to 4 days |
| 3 | Repository | `Repository[T]`, tags, all methods in 3.3, `ForFeature`, `Page` | 3 days |
| 4 | Testing and observability | `sqltest`, hooks, slow log, `observe.SQLHook` and `SQLPool`, replica routing, benchmarks | 2 to 3 days |
| 5 | Migrations and CLI | `sql/migrate`, `dygo new --db`, `dygo g crud --db`, `dygo g migration`, golden files | 3 days |
| 6 | Docs and dogfooding | section 10, `examples/academy` moved to Postgres, CHANGELOG, tag `sql/v0.1.0` | 2 days |

About three weeks in total. Milestones 1 to 3 are usable on their own and can
ship as `sql/v0.1.0-beta.1`.

### Test plan

- Unit tests with a fake `database/sql` driver for scan plans, placeholder
  rewrite (fuzzed, like the binder), SQL text per dialect, error mapping.
- Integration tests against real MySQL and Postgres. Locally they run when
  `DYGO_TEST_MYSQL_URL` or `DYGO_TEST_POSTGRES_URL` is set and skip otherwise,
  so `make test` needs no Docker, like phase 6. CI starts both as GitHub
  Actions services (two MySQL and two Postgres versions).
- A shared test suite runs the same cases on both dialects, so behaviour stays
  the same on MySQL and Postgres.

## 10. Documentation plan

| Doc | Change |
|---|---|
| `docs/guide-database.md` (new, in the site nav under Guides) | connecting, config table, custom queries, repositories, transactions, errors, several databases, replicas, testing, migrations, observability, performance notes |
| `docs/recipes/sql.md` | rewritten around the module; the current hand written module stays as "Without the sql module" |
| `docs/getting-started.md` | optional "Add a database" step after the feature module |
| `docs/guide-cli.md` | `--db`, `g migration` |
| `docs/guide-production.md` | pool sizing, `wait_timeout`, readiness, migrations in deploys |
| `sql/doc.go` and `Example` tests | compiled examples for every section of 3, shown on pkg.go.dev |
| `docs/decisions.md` | D53 onwards for the decisions below once chosen |
| `PLAN.md`, `docs/README.md`, `README.md`, `CHANGELOG.md` | roadmap entry, status, feature list |

## 11. Decisions

1. **Transactions through `ctx` or passed explicitly.** *Decided by Dylan
   (2026-10-31): through `ctx`*, with the guardrails in section 3.4. Reason:
   `Repository[T]` and generated CRUD services would otherwise need a
   transaction parameter on every method. The current SQL recipe, which says not to carry
   a transaction in `ctx`, is rewritten to match. Rejected: passing a
   `sql.Querier` or `repo.WithTx(tx)` by hand (explicit and compiler checked,
   but noisier). This is D53.
2. **Package name.** *Decided by Dylan (2026-10-31): `github.com/dygoframework/dygo/sql`*,
   as PLAN.md section 7 says. It clashes with `database/sql` when a file needs
   both, so the package re-exports what users need (`ErrNotFound`,
   `NullString`, `TxOptions`) and most files import only one. Rejected:
   `dygo/db`. This is D54.
3. **Migrations.** *Decided by Dylan (2026-10-31): the small built in runner*
   in section 5 (`sql/migrate`, embedded up and down files, a lock so one
   instance migrates). Rejected: wrapping goose or golang-migrate (more
   features, but one more dependency and its release cadence). This is D55.
4. **Placeholders.** *Decided by Dylan (2026-10-31): always `?`*, rewritten
   to `$1, $2` for Postgres (section 3.2), so the same query runs on MySQL and
   Postgres. Rejected: each database's native placeholders. This is D56.
5. **Release.** *Decided by Dylan (2026-10-31):* ship milestones 1 to 3 as
   `sql/v0.1.0-beta.1`, then the rest as `v0.1.0`; the module stays v0 until
   it has run in a real service. Rejected: one release when everything is
   done.
