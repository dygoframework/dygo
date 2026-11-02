// Package suite holds the integration tests every dialect must pass against a
// real database, so MySQL and Postgres behave the same.
package suite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/sql"
)

// Open connects to the database in the environment variable env, or skips the
// test when it is not set, so `make test` needs no database.
func Open(t *testing.T, d sql.Dialect, env string) *sql.DB {
	t.Helper()
	url := dygotest.RequireEnv(t, env)
	db, err := sql.Open(context.Background(), d, sql.Config{URL: url, MaxOpen: 4, ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Run runs the shared cases. ddl holds the dialect's CREATE TABLE statements
// for the parent and child tables, with %s for the table name suffix.
func Run(t *testing.T, db *sql.DB, parentDDL, childDDL string) {
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	parent, child := "dygo_parent_"+suffix, "dygo_child_"+suffix
	exec := func(q string, args ...any) error {
		_, err := db.Exec(ctx, q, args...)
		return err
	}
	must := func(q string, args ...any) {
		t.Helper()
		if err := exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	must(fmt.Sprintf(parentDDL, parent))
	t.Cleanup(func() { _ = exec("DROP TABLE " + parent) })
	must(fmt.Sprintf(childDDL, child, parent))
	t.Cleanup(func() { _ = exec("DROP TABLE " + child) })

	must(("INSERT INTO " + parent + " (id, email) VALUES (?, ?)"), 1, "a@example.com")

	t.Run("duplicate", func(t *testing.T) {
		err := exec(("INSERT INTO " + parent + " (id, email) VALUES (?, ?)"), 2, "a@example.com")
		if !errors.Is(err, sql.ErrDuplicate) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("foreign key", func(t *testing.T) {
		err := exec(("INSERT INTO " + child + " (id, parent_id) VALUES (?, ?)"), 1, 99)
		if !errors.Is(err, sql.ErrForeignKey) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not null", func(t *testing.T) {
		err := exec(("INSERT INTO " + parent + " (id, email) VALUES (?, ?)"), 3, nil)
		if !errors.Is(err, sql.ErrConstraint) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no rows", func(t *testing.T) {
		var email string
		err := db.Get(ctx, &email, "SELECT email FROM "+parent+" WHERE id = ?", 42)
		if !errors.Is(err, sql.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("get and select", func(t *testing.T) {
		must("INSERT INTO "+parent+" (id, email) VALUES (?, ?), (?, ?)", 10, "b@example.com", 11, "c@example.com")
		var row struct {
			ID    int64
			Email string
		}
		if err := db.Get(ctx, &row, "SELECT id, email FROM "+parent+" WHERE email = ?", "b@example.com"); err != nil || row.ID != 10 {
			t.Fatalf("row = %+v, %v", row, err)
		}
		var emails []string
		if err := db.Select(ctx, &emails, "SELECT email FROM "+parent+" WHERE id IN (?) ORDER BY id", sql.In([]int64{10, 11})); err != nil {
			t.Fatal(err)
		}
		if strings.Join(emails, ",") != "b@example.com,c@example.com" {
			t.Fatalf("emails = %v", emails)
		}
		var lit string
		if err := db.Get(ctx, &lit, "SELECT '?' AS q"); err != nil || lit != "?" {
			t.Fatalf("literal ? = %q, %v", lit, err)
		}
	})
	t.Run("transactions", func(t *testing.T) {
		count := func(ctx context.Context) int {
			var n int
			if err := db.Get(ctx, &n, "SELECT COUNT(*) FROM "+parent+" WHERE id >= ?", 100); err != nil {
				t.Fatal(err)
			}
			return n
		}
		boom := errors.New("boom")
		err := db.Tx(ctx, func(ctx context.Context) error {
			// exec uses the outer ctx, so this insert runs on the pool and
			// is committed on its own, outside the transaction.
			if err := exec("INSERT INTO "+parent+" (id, email) VALUES (?, ?)", 100, "t1@example.com"); err != nil {
				return err
			}
			if _, err := db.Exec(ctx, "INSERT INTO "+parent+" (id, email) VALUES (?, ?)", 101, "t2@example.com"); err != nil {
				return err
			}
			if got := count(ctx); got != 2 {
				t.Errorf("inside tx: %d rows", got)
			}
			// A failing savepoint undoes only its own insert.
			if err := db.Tx(ctx, func(ctx context.Context) error {
				if _, err := db.Exec(ctx, "INSERT INTO "+parent+" (id, email) VALUES (?, ?)", 102, "t3@example.com"); err != nil {
					return err
				}
				return boom
			}); !errors.Is(err, boom) {
				t.Errorf("savepoint err = %v", err)
			}
			if got := count(ctx); got != 2 {
				t.Errorf("after savepoint rollback: %d rows", got)
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		// Row 100 went through the pool, not the transaction, so it stays.
		if got := count(ctx); got != 1 {
			t.Fatalf("after rollback: %d rows, want 1", got)
		}
		if err := db.Tx(ctx, func(ctx context.Context) error {
			_, err := db.Exec(ctx, "INSERT INTO "+parent+" (id, email) VALUES (?, ?)", 103, "t4@example.com")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got := count(ctx); got != 2 {
			t.Fatalf("after commit: %d rows, want 2", got)
		}
	})
	t.Run("ping", func(t *testing.T) {
		if err := db.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

// Account is the entity of the repository cases.
type Account struct {
	ID        int64     `db:"id,pk,auto"`
	Email     string    `db:"email"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at,created"`
	UpdatedAt time.Time `db:"updated_at,updated"`
}

// plainAccount maps the same table without timestamps, so an Update can
// write exactly the values already stored.
type plainAccount struct {
	ID    int64  `db:"id,pk,auto"`
	Email string `db:"email"`
	Name  string `db:"name"`
}

// RunRepository runs the Repository cases. ddl is the dialect's CREATE TABLE
// for Account, with %s for the table name: an auto increment id, a unique
// email, a name, and created_at and updated_at with microseconds.
func RunRepository(t *testing.T, db *sql.DB, ddl string) {
	ctx := context.Background()
	table := fmt.Sprintf("dygo_accounts_%d", time.Now().UnixNano())
	if _, err := db.Exec(ctx, fmt.Sprintf(ddl, table)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(ctx, "DROP TABLE "+table) })
	users, err := sql.NewRepository[Account](db, table)
	if err != nil {
		t.Fatal(err)
	}

	a := Account{Email: "a@example.com", Name: "Ann"}
	if err := users.Create(ctx, &a); err != nil || a.ID == 0 {
		t.Fatalf("Create: %+v, %v", a, err)
	}
	got, err := users.Find(ctx, a.ID)
	if err != nil || got.Email != a.Email || !got.CreatedAt.Equal(a.CreatedAt) || !got.UpdatedAt.Equal(a.UpdatedAt) {
		t.Fatalf("Find = %+v, %v; created %+v", got, err, a)
	}
	if err := users.Create(ctx, &Account{Email: "a@example.com"}); !errors.Is(err, sql.ErrDuplicate) {
		t.Fatalf("duplicate Create: %v", err)
	}
	if _, err := users.Find(ctx, a.ID+1000); !errors.Is(err, sql.ErrNotFound) {
		t.Fatalf("Find missing: %v", err)
	}

	many := []Account{{Email: "b@example.com", Name: "Bo"}, {Email: "c@example.com", Name: "Cy"}, {Email: "d@example.com", Name: "Di"}}
	if err := users.CreateMany(ctx, many); err != nil {
		t.Fatal(err)
	}
	if db.Dialect().Returning() && (many[0].ID == 0 || many[2].ID <= many[0].ID) {
		t.Fatalf("CreateMany ids: %+v", many)
	}

	if n, err := users.Count(ctx); err != nil || n != 4 {
		t.Fatalf("Count = %d, %v", n, err)
	}
	list, err := users.List(ctx, sql.Where("email <> ?", "a@example.com"), sql.OrderBy("email DESC"), sql.Limit(2))
	if err != nil || len(list) != 2 || list[0].Email != "d@example.com" || list[1].Email != "c@example.com" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if rest, err := users.List(ctx, sql.OrderBy("email"), sql.Offset(3)); err != nil || len(rest) != 1 || rest[0].Email != "d@example.com" {
		t.Fatalf("List with Offset only = %+v, %v", rest, err)
	}
	p, err := users.Page(ctx, 2, 3, sql.OrderBy("email"))
	if err != nil || p.Total != 4 || len(p.Items) != 1 || p.Pages() != 2 || p.Items[0].Email != "d@example.com" {
		t.Fatalf("Page = %+v, %v", p, err)
	}
	if ok, err := users.Exists(ctx, sql.Where("email = ?", "c@example.com")); err != nil || !ok {
		t.Fatalf("Exists = %v, %v", ok, err)
	}
	if ok, err := users.Exists(ctx, sql.Where("email = ?", "zz@example.com")); err != nil || ok {
		t.Fatalf("Exists missing = %v, %v", ok, err)
	}
	if u, err := users.FindBy(ctx, sql.Where("name = ?", "Bo")); err != nil || u.Email != "b@example.com" {
		t.Fatalf("FindBy = %+v, %v", u, err)
	}

	a.Name = "Anna"
	before := a.UpdatedAt
	if err := users.Update(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if err := users.Patch(ctx, a.ID, sql.Set{"email": "ann@example.com"}); err != nil {
		t.Fatal(err)
	}
	got, err = users.Find(ctx, a.ID)
	if err != nil || got.Name != "Anna" || got.Email != "ann@example.com" || got.UpdatedAt.Before(before) || !got.CreatedAt.Equal(a.CreatedAt) {
		t.Fatalf("after Update and Patch: %+v, %v", got, err)
	}
	if err := users.Patch(ctx, a.ID, sql.Set{"email": "b@example.com"}); !errors.Is(err, sql.ErrDuplicate) {
		t.Fatalf("duplicate Patch: %v", err)
	}
	if err := users.Patch(ctx, a.ID+1000, sql.Set{"name": "x"}); !errors.Is(err, sql.ErrNotFound) {
		t.Fatalf("Patch missing: %v", err)
	}

	// Writing the values already stored still finds the row (MySQL needs
	// clientFoundRows for this).
	plain, err := sql.NewRepository[plainAccount](db, table)
	if err != nil {
		t.Fatal(err)
	}
	same := plainAccount{ID: a.ID, Email: "ann@example.com", Name: "Anna"}
	if err := plain.Update(ctx, &same); err != nil {
		t.Fatalf("Update with unchanged values: %v", err)
	}

	// Repositories join the transaction in ctx.
	boom := errors.New("boom")
	err = db.Tx(ctx, func(ctx context.Context) error {
		if err := users.Create(ctx, &Account{Email: "tx@example.com"}); err != nil {
			return err
		}
		if err := users.Delete(ctx, a.ID); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if n, _ := users.Count(ctx); n != 4 {
		t.Fatalf("after rollback: %d rows, want 4", n)
	}

	if err := users.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := users.Delete(ctx, a.ID); !errors.Is(err, sql.ErrNotFound) {
		t.Fatalf("second Delete: %v", err)
	}
}
