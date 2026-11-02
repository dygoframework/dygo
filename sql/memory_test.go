package sql_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/sql"
)

func TestMemoryRepository(t *testing.T) {
	ctx := context.Background()
	db := sql.Memory()
	users := newAccounts(t, db)

	for _, name := range []string{"Cid", "Ann", "Bob"} {
		a := account{Email: strings.ToLower(name) + "@x.io", Name: name, ID: 99}
		if err := users.Create(ctx, &a); err != nil || a.CreatedAt.IsZero() {
			t.Fatalf("Create %s = %+v, %v", name, a, err)
		}
	}
	if a, err := users.Find(ctx, 2); err != nil || a.Name != "Ann" || a.ID != 2 {
		t.Fatalf("Find(2) = %+v, %v", a, err)
	}
	if _, err := users.Find(ctx, 7); !errors.Is(err, sql.ErrNotFound) {
		t.Fatalf("Find(7) = %v", err)
	}
	list, err := users.List(ctx, sql.OrderBy("name DESC"), sql.Limit(2))
	if err != nil || len(list) != 2 || list[0].Name != "Cid" || list[1].Name != "Bob" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if list, _ := users.List(ctx); len(list) != 3 || list[0].ID != 1 {
		t.Fatalf("List() = %+v", list)
	}
	if p, err := users.Page(ctx, 2, 2); err != nil || p.Total != 3 || len(p.Items) != 1 || p.Items[0].Name != "Bob" {
		t.Fatalf("Page = %+v, %v", p, err)
	}
	if n, err := users.Count(ctx); n != 3 || err != nil {
		t.Fatalf("Count = %d, %v", n, err)
	}

	a, _ := users.Find(ctx, 1)
	created := a.CreatedAt
	a.Name, a.CreatedAt = "Cyd", created.AddDate(1, 0, 0)
	if err := users.Update(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if a, _ := users.Find(ctx, 1); a.Name != "Cyd" || !a.CreatedAt.Equal(created) {
		t.Fatalf("after Update %+v", a)
	}
	if err := users.Patch(ctx, int64(1), sql.Set{"email": "cyd@x.io"}); err != nil {
		t.Fatal(err)
	}
	if a, _ := users.Find(ctx, uint(1)); a.Email != "cyd@x.io" || a.Name != "Cyd" {
		t.Fatalf("after Patch %+v", a)
	}
	if err := users.Patch(ctx, 1, sql.Set{"nope": 1}); !errors.Is(err, sql.ErrUnknownColumn) {
		t.Fatalf("Patch unknown = %v", err)
	}
	if err := users.Delete(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := users.Delete(ctx, 1); !errors.Is(err, sql.ErrNotFound) {
		t.Fatalf("Delete twice = %v", err)
	}
	b := account{Name: "Dan"}
	if err := users.Create(ctx, &b); err != nil || b.ID != 4 {
		t.Fatalf("Create after Delete = %+v, %v", b, err)
	}

	// The same table name on the same database is the same data.
	if again := newAccounts(t, db); again != nil {
		if n, _ := again.Count(ctx); n != 3 {
			t.Fatalf("second repository sees %d rows", n)
		}
	}

	if _, err := users.List(ctx, sql.Where("name = ?", "Ann")); !errors.Is(err, sql.ErrMemory) {
		t.Fatalf("Where = %v", err)
	}
	var n int
	if err := db.Get(ctx, &n, "SELECT 1"); !errors.Is(err, sql.ErrMemory) {
		t.Fatalf("Get = %v", err)
	}
	if err := db.Tx(ctx, func(ctx context.Context) error { return users.Delete(ctx, 2) }); err != nil {
		t.Fatalf("Tx = %v", err)
	}
	if err := db.PingContext(ctx); err != nil || !db.IsMemory() {
		t.Fatalf("Ping = %v", err)
	}
}

func TestModuleMemoryURL(t *testing.T) {
	t.Setenv("DATABASE_URL", sql.MemoryURL)
	app := dygotest.New(&dygo.Module{Name: "app", Imports: []*dygo.Module{sql.Module(nil, sql.Files())}}).Compile(t)
	db := dygo.MustGet[*sql.DB](app)
	if !db.IsMemory() {
		t.Fatal("not in memory")
	}

	t.Setenv("DATABASE_URL", "")
	os.Unsetenv("DATABASE_URL")
	app = dygotest.New(&dygo.Module{Name: "app", Imports: []*dygo.Module{sql.Module(nil, sql.Files())}}).Compile(t)
	if !dygo.MustGet[*sql.DB](app).IsMemory() {
		t.Fatal("no DATABASE_URL: not in memory")
	}

	t.Setenv("DATABASE_URL", "postgres://u:secret@localhost/app")
	_, err := dygotest.New(&dygo.Module{Name: "app", Imports: []*dygo.Module{sql.Module(nil, sql.Files())}}).Build(context.Background())
	if err == nil || !strings.Contains(err.Error(), "dygo db init -driver postgres") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Module(nil) with a postgres URL: %v", err)
	}
}

func TestMemoryModuleNotFound(t *testing.T) {
	srv := dygotest.New(&dygo.Module{
		Name:      "app",
		Imports:   []*dygo.Module{sql.MemoryModule()},
		Providers: []dygo.Provider{sql.ForFeature[account]("accounts")},
		Controllers: []dygo.Provider{dygo.Provide(func(r *sql.Repository[account]) *memCtl {
			return &memCtl{r}
		})},
	}).CompileHTTP(t)
	srv.GET("/accounts/5").Do(t).Status(t, http.StatusNotFound)
}

type memCtl struct{ r *sql.Repository[account] }

func (c *memCtl) Routes(r dygo.Router) {
	r.Get("/accounts/{id}", dygo.Handle(func(ctx context.Context, in struct {
		ID int `path:"id"`
	}) (account, error) {
		return c.r.Find(ctx, in.ID)
	}))
}
