package sql_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/dygotest"
	"github.com/dygoframework/dygo/errs"
	"github.com/dygoframework/dygo/sql"
)

type account struct {
	ID        int64     `db:"id,pk,auto"`
	Email     string    `db:"email"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at,created"`
	UpdatedAt time.Time `db:"updated_at,updated"`
	Note      string    `db:"-"`
}

func newAccounts(t *testing.T, db *sql.DB) *sql.Repository[account] {
	t.Helper()
	r, err := sql.NewRepository[account](db, "accounts")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var accountCols = []string{"id", "email", "name", "created_at", "updated_at"}

func TestRepositoryReads(t *testing.T) {
	ctx := context.Background()
	sel := "SELECT `id`, `email`, `name`, `created_at`, `updated_at` FROM `accounts`"
	row := []driver.Value{int64(1), "a@x.io", "Ann", now, now}
	db := openRec(t, map[string]recResult{
		sel + " WHERE `id` = ?":            {cols: accountCols, rows: [][]driver.Value{row}},
		sel + " WHERE (email = ?) LIMIT 1": {cols: accountCols, rows: [][]driver.Value{row}},
		sel + " WHERE (name LIKE ?) AND (id > ?) ORDER BY `created_at` DESC, `id` LIMIT 2 OFFSET 4": {
			cols: accountCols, rows: [][]driver.Value{row, row},
		},
		sel + " LIMIT 9223372036854775807 OFFSET 3":           {cols: accountCols},
		"SELECT COUNT(*) FROM `accounts` WHERE (name LIKE ?)": {cols: []string{"count"}, rows: [][]driver.Value{{int64(7)}}},
		"SELECT 1 FROM `accounts` WHERE (email = ?) LIMIT 1":  {cols: []string{"1"}, rows: [][]driver.Value{{int64(1)}}},
		sel + " WHERE (name LIKE ?) LIMIT 2 OFFSET 2":         {cols: accountCols, rows: [][]driver.Value{row, row}},
	})
	users := newAccounts(t, db)

	a, err := users.Find(ctx, 1)
	if err != nil || a.Email != "a@x.io" || !a.CreatedAt.Equal(now) {
		t.Fatalf("Find = %+v, %v", a, err)
	}
	if a, err := users.FindBy(ctx, sql.Where("email = ?", "a@x.io")); err != nil || a.ID != 1 {
		t.Fatalf("FindBy = %+v, %v", a, err)
	}
	list, err := users.List(ctx, sql.Where("name LIKE ?", "A%"), sql.Where("id > ?", 0),
		sql.OrderBy("created_at DESC, id"), sql.Limit(2), sql.Offset(4))
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v", list, err)
	}
	empty, err := users.List(ctx, sql.Offset(3))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("List offset only = %#v, %v", empty, err)
	}
	if n, err := users.Count(ctx, sql.Where("name LIKE ?", "A%"), sql.OrderBy("id"), sql.Limit(1)); err != nil || n != 7 {
		t.Fatalf("Count = %d, %v", n, err)
	}
	if ok, err := users.Exists(ctx, sql.Where("email = ?", "a@x.io")); err != nil || !ok {
		t.Fatalf("Exists = %v, %v", ok, err)
	}
	p, err := users.Page(ctx, 2, 2, sql.Where("name LIKE ?", "A%"), sql.Limit(100))
	if err != nil || p.Total != 7 || len(p.Items) != 2 || p.Page != 2 || p.Size != 2 || p.Pages() != 4 {
		t.Fatalf("Page = %+v, %v", p, err)
	}
	if _, err := users.Page(ctx, 1, 0); err == nil {
		t.Fatal("Page size 0: want error")
	}
	got := rec.take()
	if !slices.Contains(got, sel+" WHERE (name LIKE ?) LIMIT 2 OFFSET 2") {
		t.Fatalf("Page query missing from %q", got)
	}
}

func TestRepositoryOrderByChecksColumns(t *testing.T) {
	db := openRec(t, nil)
	users := newAccounts(t, db)
	for _, o := range []string{"password", "id; DROP TABLE accounts", "id DESCENDING", "id asc extra", ""} {
		_, err := users.List(context.Background(), sql.OrderBy(o))
		if !errors.Is(err, sql.ErrUnknownColumn) {
			t.Errorf("OrderBy(%q): err = %v", o, err)
		}
		var c errs.Converter
		if !errors.As(err, &c) || c.HTTPError().Status != http.StatusBadRequest {
			t.Errorf("OrderBy(%q): want a 400", o)
		}
	}
	if len(rec.take()) != 0 {
		t.Fatal("a query ran")
	}
}

func TestRepositoryWritesMySQLStyle(t *testing.T) {
	ctx := context.Background()
	db := openRec(t, nil)
	users := newAccounts(t, db)

	a := account{ID: 99, Email: "a@x.io", Name: "Ann", Note: "skip"}
	if err := users.Create(ctx, &a); err != nil {
		t.Fatal(err)
	}
	if a.ID != 42 || a.CreatedAt.IsZero() || !a.UpdatedAt.Equal(a.CreatedAt) || a.CreatedAt.Location() != time.UTC {
		t.Fatalf("after Create: %+v", a)
	}
	ins := "INSERT INTO `accounts` (`email`, `name`, `created_at`, `updated_at`) VALUES (?, ?, ?, ?)"
	if got := rec.take(); len(got) != 1 || got[0] != ins {
		t.Fatalf("Create ran %q", got)
	}
	if args := rec.takeArgs(); len(args[0]) != 4 || args[0][0] != "a@x.io" {
		t.Fatalf("Create args %v", args)
	}

	created := a.CreatedAt
	time.Sleep(time.Millisecond)
	a.Name = "Anna"
	if err := users.Update(ctx, &a); err != nil {
		t.Fatal(err)
	}
	upd := "UPDATE `accounts` SET `email` = ?, `name` = ?, `updated_at` = ? WHERE `id` = ?"
	if got := rec.take(); got[0] != upd {
		t.Fatalf("Update ran %q", got)
	}
	if args := rec.takeArgs(); args[0][1] != "Anna" || args[0][3] != int64(42) {
		t.Fatalf("Update args %v", args)
	}
	if !a.CreatedAt.Equal(created) || !a.UpdatedAt.After(created) {
		t.Fatalf("Update stamps: %+v", a)
	}

	if err := users.Patch(ctx, 42, sql.Set{"name": "Bo", "email": "b@x.io"}); err != nil {
		t.Fatal(err)
	}
	patch := "UPDATE `accounts` SET `email` = ?, `name` = ?, `updated_at` = ? WHERE `id` = ?"
	if got := rec.take(); got[0] != patch {
		t.Fatalf("Patch ran %q", got)
	}
	for _, bad := range []sql.Set{{"id": 1}, {"password": "x"}, {"Note": "x"}} {
		if err := users.Patch(ctx, 42, bad); !errors.Is(err, sql.ErrUnknownColumn) {
			t.Errorf("Patch(%v) err = %v", bad, err)
		}
	}

	if err := users.Delete(ctx, 42); err != nil {
		t.Fatal(err)
	}
	del := "DELETE FROM `accounts` WHERE `id` = ?"
	rec.zero[del] = true
	rec.zero[upd] = true
	if err := users.Delete(ctx, 7); !errors.Is(err, sql.ErrNotFound) {
		t.Fatalf("Delete missing: %v", err)
	}
	if err := users.Update(ctx, &a); !errors.Is(err, sql.ErrNotFound) {
		t.Fatalf("Update missing: %v", err)
	}
	rec.take()

	many := []account{{Email: "c@x.io"}, {Email: "d@x.io"}}
	if err := users.CreateMany(ctx, many); err != nil {
		t.Fatal(err)
	}
	if got := rec.take(); len(got) != 1 || got[0] != strings.TrimSuffix(ins, "(?, ?, ?, ?)")+"(?, ?, ?, ?), (?, ?, ?, ?)" {
		t.Fatalf("CreateMany ran %q", got)
	}
	if many[1].CreatedAt.IsZero() || many[1].ID != 0 {
		t.Fatalf("CreateMany items %+v", many)
	}
}

func TestRepositoryWritesPostgresStyle(t *testing.T) {
	ctx := context.Background()
	ins := `INSERT INTO "accounts" ("email", "name", "created_at", "updated_at") VALUES `
	db := openRecWith(t, recPG{}, map[string]recResult{
		ins + `($1, $2, $3, $4) RETURNING "id"`: {cols: []string{"id"}, rows: [][]driver.Value{{int64(7)}}},
		ins + `($1, $2, $3, $4), ($5, $6, $7, $8), ($9, $10, $11, $12) RETURNING "id"`: {
			cols: []string{"id"}, rows: [][]driver.Value{{int64(10)}, {int64(11)}, {int64(12)}},
		},
	})
	users := newAccounts(t, db)
	a := account{Email: "a@x.io"}
	if err := users.Create(ctx, &a); err != nil || a.ID != 7 {
		t.Fatalf("Create: %+v, %v", a, err)
	}
	many := make([]account, 3)
	if err := users.CreateMany(ctx, many); err != nil {
		t.Fatal(err)
	}
	if many[0].ID != 10 || many[2].ID != 12 {
		t.Fatalf("CreateMany ids %+v", many)
	}
	if err := users.Patch(ctx, 7, sql.Set{"name": "x"}); err != nil {
		t.Fatal(err)
	}
	got := rec.take()
	if got[len(got)-1] != `UPDATE "accounts" SET "name" = $1, "updated_at" = $2 WHERE "id" = $3` {
		t.Fatalf("Patch ran %q", got[len(got)-1])
	}
}

func TestCreateManyBatchesInATransaction(t *testing.T) {
	db := openRec(t, nil)
	users := newAccounts(t, db)
	many := make([]account, 2500)
	if err := users.CreateMany(context.Background(), many); err != nil {
		t.Fatal(err)
	}
	got := rec.take()
	if len(got) != 5 || got[0] != "BEGIN" || got[4] != "COMMIT" {
		t.Fatalf("ran %d statements, first %q", len(got), got[0])
	}
	if n := strings.Count(got[3], "(?, ?, ?, ?)"); n != 500 {
		t.Fatalf("last batch has %d rows", n)
	}
}

func TestRepositoryInTx(t *testing.T) {
	db := openRec(t, nil)
	users := newAccounts(t, db)
	err := db.Tx(context.Background(), func(ctx context.Context) error {
		if err := users.Create(ctx, &account{Email: "a"}); err != nil {
			return err
		}
		return users.Delete(ctx, 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.take(); len(got) != 4 || got[0] != "BEGIN" || got[3] != "COMMIT" {
		t.Fatalf("ran %q", got)
	}
}

func TestRepositoryTagErrors(t *testing.T) {
	db := openRec(t, nil)
	type noPK struct {
		ID int64 `db:"id"`
	}
	type twoAuto struct {
		ID  int64 `db:"id,pk,auto"`
		Seq int64 `db:"seq,auto"`
		X   string
	}
	type strAuto struct {
		ID string `db:"id,pk,auto"`
		X  string
	}
	type badStamp struct {
		ID int64  `db:"id,pk"`
		At string `db:"at,created"`
	}
	type badOpt struct {
		ID int64 `db:"id,primary"`
	}
	type onlyAuto struct {
		ID int64 `db:"id,pk,auto"`
	}
	check := func(name string, err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
	_, err := sql.NewRepository[noPK](db, "t")
	check("noPK", err, "exactly one primary key")
	_, err = sql.NewRepository[twoAuto](db, "t")
	check("twoAuto", err, "one auto column")
	_, err = sql.NewRepository[strAuto](db, "t")
	check("strAuto", err, "must be an integer")
	_, err = sql.NewRepository[badStamp](db, "t")
	check("badStamp", err, "must be a time.Time")
	_, err = sql.NewRepository[badOpt](db, "t")
	check("badOpt", err, `unknown option "primary"`)
	_, err = sql.NewRepository[onlyAuto](db, "t")
	check("onlyAuto", err, "nothing to write")
	_, err = sql.NewRepository[int](db, "t")
	check("int", err, "must be a struct")

	// Postgres reads auto columns with RETURNING, so any number and type works.
	pg := openRecWith(t, recPG{}, nil)
	if _, err := sql.NewRepository[twoAuto](pg, "t"); err != nil {
		t.Fatal(err)
	}
}

type accountService struct{ users *sql.Repository[account] }

func TestForFeature(t *testing.T) {
	db := openRec(t, nil)
	feature := &dygo.Module{
		Name: "accounts",
		Providers: []dygo.Provider{
			sql.ForFeature[account]("accounts"),
			dygo.Provide(func(r *sql.Repository[account]) *accountService { return &accountService{r} }),
		},
		Exports: []dygo.Token{dygo.TypeOf[*accountService]()},
	}
	root := &dygo.Module{
		Name: "app",
		Imports: []*dygo.Module{
			{Name: "db", Global: true, Providers: []dygo.Provider{dygo.Value(db)}, Exports: []dygo.Token{dygo.TypeOf[*sql.DB]()}},
			feature,
		},
	}
	app := dygotest.New(root).Compile(t)
	if s := dygo.MustGet[*accountService](app); s.users.Table() != "accounts" || s.users.DB() != db {
		t.Fatal("repository not wired to the pool")
	}
}

func TestForFeatureUsing(t *testing.T) {
	db := openRec(t, nil)
	root := &dygo.Module{Name: "app", Providers: []dygo.Provider{
		dygo.Value(db, dygo.For(sql.Token("reports"))),
		sql.ForFeature[account]("accounts", sql.Using("reports")),
	}}
	app := dygotest.New(root).Compile(t)
	if r := dygo.MustGet[*sql.Repository[account]](app); r.DB() != db {
		t.Fatal("repository not on the named pool")
	}

	// A missing named pool is a boot error that names it.
	bad := &dygo.Module{Name: "bad", Providers: []dygo.Provider{sql.ForFeature[account]("accounts", sql.Using("nope"))}}
	if _, err := dygo.CreateApplicationContext(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}
