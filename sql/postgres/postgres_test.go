package postgres_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dygoframework/dygo/sql"
	"github.com/dygoframework/dygo/sql/internal/suite"
	"github.com/dygoframework/dygo/sql/postgres"
)

func TestDSN(t *testing.T) {
	d := postgres.Dialect()
	for _, ok := range []string{
		"postgres://u:p@localhost:5432/app?sslmode=disable",
		"postgresql://localhost/app",
		"host=localhost user=u dbname=app",
	} {
		if got, err := d.DSN(ok); err != nil || got != ok {
			t.Errorf("DSN(%q) = %q, %v", ok, got, err)
		}
	}
	if _, err := d.DSN("postgres://u:p@localhost:notaport/app"); err == nil {
		t.Error("bad port: want error")
	}
}

func TestClassify(t *testing.T) {
	d := postgres.Dialect()
	cases := []struct {
		code, constraint, column string
		kind                     sql.Kind
		want                     string
	}{
		{"23505", "users_email_key", "", sql.KindDuplicate, "users_email_key"},
		{"23503", "orders_user_fk", "", sql.KindForeignKey, "orders_user_fk"},
		{"23502", "", "email", sql.KindConstraint, "email"},
		{"23514", "price_positive", "", sql.KindConstraint, "price_positive"},
		{"40001", "", "", sql.KindRetryable, ""},
		{"40P01", "", "", sql.KindRetryable, ""},
		{"42P01", "", "", sql.KindOther, ""},
	}
	for _, c := range cases {
		err := fmt.Errorf("exec: %w", &pgconn.PgError{Code: c.code, ConstraintName: c.constraint, ColumnName: c.column})
		kind, constraint := d.Classify(err)
		if kind != c.kind || constraint != c.want {
			t.Errorf("%s: kind %d constraint %q", c.code, kind, constraint)
		}
	}
	if kind, _ := d.Classify(errors.New("other")); kind != sql.KindOther {
		t.Error("plain error must be KindOther")
	}
}

func TestIntegration(t *testing.T) {
	db := suite.Open(t, postgres.Dialect(), "DYGO_TEST_POSTGRES_URL")
	suite.Run(t, db,
		`CREATE TABLE %s (id BIGINT PRIMARY KEY, email TEXT NOT NULL UNIQUE)`,
		`CREATE TABLE %s (id BIGINT PRIMARY KEY, parent_id BIGINT NOT NULL REFERENCES %s (id))`)
	t.Run("repository", func(t *testing.T) {
		suite.RunRepository(t, db,
			`CREATE TABLE %s (id BIGSERIAL PRIMARY KEY, email TEXT NOT NULL UNIQUE, name TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`)
	})
}
