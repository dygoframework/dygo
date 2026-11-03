# Recipe: SQL databases

Dygo has no ORM layer of its own. A database is a provider like any other: a
constructor that opens the connection, a lifecycle hook that closes it, and
repositories that take it as a dependency. This page uses `database/sql` with
Postgres; the same shape works for pgx pools, GORM or ent.

```sh
go get github.com/jackc/pgx/v5
```

## The connection module

A global module owns the pool, so every feature can inject it. The connection
is opened and pinged during boot, so a wrong URL stops the app before it
listens instead of failing the first request.

```go
package database

import (
    "context"
    "database/sql"
    "time"

    _ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver

    "github.com/dygoframework/dygo"
    "github.com/dygoframework/dygo/config"
    "github.com/dygoframework/dygo/health"
)

type Config struct {
    URL      string `env:"DATABASE_URL" required:"true"`
    MaxConns int    `env:"DATABASE_MAX_CONNS" default:"10"`
}

// DB wraps *sql.DB so the container can close it on shutdown.
type DB struct{ *sql.DB }

func Open(ctx context.Context, cfg *Config) (*DB, error) {
    db, err := sql.Open("pgx", cfg.URL)
    if err != nil {
        return nil, err
    }
    db.SetMaxOpenConns(cfg.MaxConns)
    db.SetConnMaxIdleTime(5 * time.Minute)
    if err := db.PingContext(ctx); err != nil { // fail the boot, not the first request
        db.Close()
        return nil, err
    }
    return &DB{db}, nil
}

// OnApplicationShutdown runs after in-flight requests have finished. A test
// can override *DB with &database.DB{}, which has nothing to close.
func (db *DB) OnApplicationShutdown(context.Context, string) error {
    if db.DB == nil {
        return nil
    }
    return db.Close()
}

var Module = &dygo.Module{
    Name:    "database",
    Global:  true,
    Imports: []*dygo.Module{config.ForFeature[Config](config.Files(".env"))},
    Providers: []dygo.Provider{
        dygo.Provide(Open),
        dygo.Provide(func(db *DB) health.Indicator { return health.Ping("db", db) }, dygo.Group(health.Indicators)),
    },
    Exports: []dygo.Token{dygo.TypeOf[*DB]()},
}

// InTx runs fn in a transaction: commit when fn returns nil, roll back otherwise.
func (db *DB) InTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
    tx, err := db.BeginTx(ctx, nil)
    if err != nil {
        return err
    }
    if err := fn(tx); err != nil {
        _ = tx.Rollback()
        return err
    }
    return tx.Commit()
}
```

`OnApplicationShutdown` runs after the HTTP server has drained in-flight
requests, so no handler loses its connection mid-query. The health indicator
makes `GET /health/ready` answer 503 when the database is down.

## From memory to a database

`.env` decides where the data goes. An app made by `dygo new` starts with
`DATABASE_URL=memory://` and imports `sql.Module(nil)` (the `database`
package in the modular layout), so the services `dygo g crud` and
`dygo g resource` write keep their data in memory, and it is lost when the
app stops. Those services use `*sql.Repository[T]`, so the same code works on
a real database:

1. `dygo db init -driver postgres` (once per app) adds the driver and puts the
   database URL in `.env`.
2. `dygo db create`, `dygo g migration` (it writes the `CREATE TABLE` for
   every entity) and `dygo db migrate`.
3. Start the app. The log says which database it uses:
   `database connected url=postgres://...` or `database in memory`.

Setting `DATABASE_URL=memory://` again goes back to memory, with no code
change. In memory, `sql.Where` clauses and raw SQL fail with `sql.ErrMemory`;
they need a real database. Feature tests use `sql.MemoryModule()`, so they
never need one.

## A repository

The `sql` module gives a ready-made one: `sql.ForFeature[User]("users")`
provides `*sql.Repository[User]` with `Find`, `List`, `Page`, `Create`,
`Update`, `Patch` and `Delete`, built from the struct's `db` tags (see
`sql/doc.go`). The hand written version below shows the same idea without the
module, for pgx pools, GORM or ent.

Define the interface where it is used, and bind the Postgres implementation to
it with `dygo.As`. Map `sql.ErrNoRows` to your own error, so the controller does
not depend on `database/sql`.

```go
type User struct {
    ID    int64  `json:"id"`
    Email string `json:"email"`
}

// Repo is what the service needs. Tests replace it with a fake.
type Repo interface {
    Find(ctx context.Context, id int64) (User, error)
    Create(ctx context.Context, email string) (User, error)
}

var ErrNotFound = errors.New("user not found")

type PgRepo struct{ db *database.DB }

func NewPgRepo(db *database.DB) *PgRepo { return &PgRepo{db: db} }

func (r *PgRepo) Find(ctx context.Context, id int64) (User, error) {
    var u User
    err := r.db.QueryRowContext(ctx, `SELECT id, email FROM users WHERE id = $1`, id).Scan(&u.ID, &u.Email)
    if errors.Is(err, sql.ErrNoRows) {
        return User{}, ErrNotFound
    }
    return u, err
}

func (r *PgRepo) Create(ctx context.Context, email string) (User, error) {
    u := User{Email: email}
    err := r.db.InTx(ctx, func(tx *sql.Tx) error {
        if err := tx.QueryRowContext(ctx, `INSERT INTO users (email) VALUES ($1) RETURNING id`, email).Scan(&u.ID); err != nil {
            return err
        }
        _, err := tx.ExecContext(ctx, `INSERT INTO audit (user_id, action) VALUES ($1, 'created')`, u.ID)
        return err
    })
    return u, err
}
```

## Using it from a controller

```go
func (c *Controller) Get(ctx context.Context, in GetIn) (User, error) {
    u, err := c.repo.Find(ctx, in.ID)
    if errors.Is(err, ErrNotFound) {
        return User{}, errs.NotFound("user not found")
    }
    return u, err // any other error is a logged 500
}

var Module = &dygo.Module{
    Name:        "users",
    Providers:   []dygo.Provider{dygo.Provide(NewPgRepo, dygo.As[Repo]())},
    Controllers: []dygo.Provider{dygo.Provide(NewController)},
}
```

Errors that are not `*errs.HTTPError` become a 500 with a generic body, and
the real error (with the SQL detail) is logged, never sent to the client.

## Transactions

The hand written `InTx` above passes the `*sql.Tx` explicitly. The `sql`
module (`github.com/dygoframework/dygo/sql`) takes the other route:
`db.Tx(ctx, fn)` puts the transaction in `ctx`, and every
query that gets that `ctx` joins it, nested calls become savepoints, and
`sql.Detach(ctx)` gives goroutines a context without it. Pick one style per
app.

## Migrations

Run migrations outside the request path. Two common choices:

- a console command (`dygo g command migrate`, using the `console` package)
  that runs [golang-migrate](https://github.com/golang-migrate/migrate) or
  [goose](https://github.com/pressly/goose), started as a separate step in
  your deploy;
- an `OnApplicationBootstrap` hook on a `Migrator` provider, for small
  services with one instance.

## Testing

Unit test controllers with a fake repository. The test root imports the same
modules as the app and replaces the config and the connection, so nothing
dials Postgres:

```go
type fakeRepo struct{ users map[int64]users.User }

func (f *fakeRepo) Find(_ context.Context, id int64) (users.User, error) {
    u, ok := f.users[id]
    if !ok {
        return users.User{}, users.ErrNotFound
    }
    return u, nil
}

func (f *fakeRepo) Create(context.Context, string) (users.User, error) { panic("unused") }

func TestGetUser(t *testing.T) {
    fake := &fakeRepo{users: map[int64]users.User{1: {ID: 1, Email: "ann@example.com"}}}
    // The app's root module brings in database.Module; a test root does the
    // same, with the config and connection replaced so nothing dials Postgres.
    root := &dygo.Module{Name: "test", Imports: []*dygo.Module{database.Module, users.Module}}
    srv := dygotest.New(root).
        OverrideValue(dygo.TypeOf[*database.Config](), &database.Config{}).
        OverrideValue(dygo.TypeOf[*database.DB](), &database.DB{}).
        OverrideValue(dygo.TypeOf[users.Repo](), fake).
        CompileHTTP(t)

    srv.GET("/users/1").Do(t).Status(t, 200)
    srv.GET("/users/2").Do(t).Status(t, 404)
}
```

For repository tests, run a real Postgres (for example with
[testcontainers-go](https://golang.testcontainers.org/)) and call the
repository directly; there is no framework code to mock.

## Several databases

Give each pool its own type (`type ReportsDB struct{ *sql.DB }`) or a named
token, so the container can tell them apart:

```go
var ReadReplica = dygo.NewToken[*sql.DB]("db.replica")

dygo.Provide(OpenReplica, dygo.For(ReadReplica))

type Params struct {
    dygo.In
    Primary *DB
    Replica *sql.DB `name:"db.replica"`
}
```

## Per-tenant databases

For a database per tenant, use a durable provider keyed by tenant. See
"Durable providers" in the [HTTP guide](../guide-http.md).
