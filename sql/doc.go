// Package sql connects a Dygo app to MySQL or Postgres. It stays close to
// database/sql and plain SQL; it is not an ORM.
//
// Import the module for your database once, in the root module:
//
//	import "github.com/dygoframework/dygo/sql/postgres" // or sql/mysql
//
//	var AppModule = &dygo.Module{
//		Imports: []*dygo.Module{postgres.Module(), users.Module},
//	}
//
// The module is global and provides *sql.DB to every provider:
//
//	func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }
//
// At boot it reads [Config] from the environment (DATABASE_URL, DB_MAX_OPEN,
// ...), opens the pool and pings the database, so a wrong URL or password
// stops the app before it listens. The password never appears in errors or
// logs. On shutdown the pool is closed after in-flight requests finish, and
// GET /health/ready reports the database as "db".
//
// Driver errors become [*Error] values with [DB.MapError]: a duplicate key
// matches ErrDuplicate with errors.Is and answers 409, a missing row matches
// ErrNotFound and answers 404, with no driver import in your controllers.
//
// Queries are written with ? on both databases, and results are scanned into
// structs by db tag (or the snake_case field name):
//
//	var u User
//	err := db.Get(ctx, &u, "SELECT id, email FROM users WHERE id = ?", id)
//	var list []User
//	err = db.Select(ctx, &list, "SELECT * FROM users WHERE id IN (?)", sql.In(ids))
//	_, err = db.Exec(ctx, "UPDATE users SET active = ? WHERE id = ?", false, id)
//
// Transactions travel in ctx: every query that gets fn's ctx joins it, and a
// nested Tx is a savepoint. Use Detach(ctx) for goroutines started inside.
//
//	err := db.Tx(ctx, func(ctx context.Context) error {
//		if err := orders.Create(ctx, o); err != nil {
//			return err // rolls back
//		}
//		return stock.Take(ctx, o.ProductID, o.Qty)
//	})
//
// For CRUD without writing SQL, map a struct with db tags and provide a
// repository in the feature module:
//
//	type User struct {
//		ID        int64     `db:"id,pk,auto"`
//		Email     string    `db:"email"`
//		CreatedAt time.Time `db:"created_at,created"`
//		UpdatedAt time.Time `db:"updated_at,updated"`
//	}
//
//	Providers: []dygo.Provider{sql.ForFeature[User]("users"), dygo.Provide(NewService)}
//
//	func NewService(users *sql.Repository[User]) *Service
//
//	u, err := users.Find(ctx, id) // ErrNotFound answers 404
//	page, err := users.Page(ctx, 1, 20, sql.Where("active = ?", true), sql.OrderBy(in.Sort))
//	err = users.Create(ctx, &u)   // sets ID, CreatedAt, UpdatedAt
//	err = users.Patch(ctx, id, sql.Set{"email": in.Email})
//
// DATABASE_URL=memory:// keeps the data in memory instead, for the start of
// a project and for tests: repositories work the same, so moving to a real
// database is changing the URL. See [MemoryURL].
//
// For a second database, give it a prefix and a name:
//
//	postgres.Module(sql.Prefix("REPORTS_"), sql.Named("reports"))
//
// and inject it with a field tagged name:"reports" in a dygo.In struct, or
// put a repository on it with sql.ForFeature[T](table, sql.Using("reports")).
//
// This package is v0: its API can still change before v1. See
// docs/phase-8-database.md for the plan.
package sql
