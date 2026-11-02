// Package postgres is the Postgres dialect for the Dygo sql module, on the
// pgx driver:
//
//	Imports: []*dygo.Module{postgres.Module()} // reads DATABASE_URL
//
// DATABASE_URL is a postgres:// URL or a key=value connection string, as pgx
// accepts them.
package postgres

import (
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/sql"
)

// Module returns sql.Module with the Postgres dialect.
func Module(opts ...sql.Option) *dygo.Module { return sql.Module(Dialect(), opts...) }

// Dialect returns the Postgres dialect, for sql.Open and tests.
func Dialect() sql.Dialect { return dialect{} }

type dialect struct{}

func (dialect) Name() string       { return "postgres" }
func (dialect) DriverName() string { return "pgx" }

// DSN checks the URL with pgx, so a malformed one fails before dialing, and
// passes it through unchanged.
func (dialect) DSN(url string) (string, error) {
	if _, err := pgx.ParseConfig(url); err != nil {
		return "", err
	}
	return url, nil
}

// Classify maps Postgres SQLSTATE codes.
func (dialect) Classify(err error) (sql.Kind, string) {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return sql.KindOther, ""
	}
	switch pe.Code {
	case "23505": // unique_violation
		return sql.KindDuplicate, pe.ConstraintName
	case "23503": // foreign_key_violation
		return sql.KindForeignKey, pe.ConstraintName
	case "23502", "23514": // not_null_violation, check_violation
		c := pe.ConstraintName
		if c == "" {
			c = pe.ColumnName
		}
		return sql.KindConstraint, c
	case "40001", "40P01": // serialization_failure, deadlock_detected
		return sql.KindRetryable, ""
	}
	return sql.KindOther, ""
}

func (dialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }

// BackslashEscapes is false: in Postgres a backslash escapes only inside
// E'...' strings, which the rewriter handles itself.
func (dialect) BackslashEscapes() bool { return false }

// Quote quotes an identifier with double quotes, part by part for a dotted
// name. A quoted name is case sensitive in Postgres, so keep column names in
// db tags lower case, as unquoted names are folded to lower case.
func (dialect) Quote(ident string) string {
	parts := strings.Split(ident, ".")
	for i, p := range parts {
		parts[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
	}
	return strings.Join(parts, ".")
}

// Returning is true: INSERT ... RETURNING reads back the columns the database
// set.
func (dialect) Returning() bool { return true }
