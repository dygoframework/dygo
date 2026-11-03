package sql

import (
	stdsql "database/sql"
	"errors"
	"fmt"

	"github.com/dygoframework/dygo/errs"
)

// Kind is the class of a database error, as reported by [Dialect.Classify].
type Kind int

// The kinds of database errors the module recognises.
const (
	// KindOther is any error the dialect does not recognise.
	KindOther Kind = iota
	// KindDuplicate is a unique or primary key violation.
	KindDuplicate
	// KindForeignKey is a foreign key violation.
	KindForeignKey
	// KindConstraint is a not null or check constraint violation.
	KindConstraint
	// KindRetryable is a serialization failure or deadlock: running the
	// transaction again may succeed.
	KindRetryable
	// KindSchema is a table or column the database does not have: the
	// migrations are behind the code.
	KindSchema
)

// Errors that mapped errors match with errors.Is.
var (
	// ErrNotFound means no row matched. It answers 404.
	ErrNotFound = errors.New("not found")
	// ErrDuplicate means a unique constraint was violated. It answers 409.
	ErrDuplicate = errors.New("duplicate")
	// ErrForeignKey means a foreign key was violated. It answers 409.
	ErrForeignKey = errors.New("foreign key violation")
	// ErrConstraint means a not null or check constraint was violated. It
	// answers 422.
	ErrConstraint = errors.New("constraint violation")
	// ErrRetryable means the transaction hit a serialization failure or a
	// deadlock and may succeed if run again. It answers 500.
	ErrRetryable = errors.New("retryable")
	// ErrSchema means a table or column the query uses does not exist, so
	// the database is behind the code. It answers 500, and its message says
	// to run dygo g migration and dygo db migrate.
	ErrSchema = errors.New("schema out of date")
)

// schemaHint ends the message of an ErrSchema error.
const schemaHint = "the database is missing a table or column the code uses; run dygo g migration, check the new file, then dygo db migrate"

func (k Kind) sentinel() error {
	switch k {
	case KindDuplicate:
		return ErrDuplicate
	case KindForeignKey:
		return ErrForeignKey
	case KindConstraint:
		return ErrConstraint
	case KindRetryable:
		return ErrRetryable
	case KindSchema:
		return ErrSchema
	}
	return nil
}

// Error is a database error the module recognised. errors.Is matches its
// kind (ErrDuplicate, ...) and errors.As still reaches the driver's error
// (*pgconn.PgError, *mysql.MySQLError). The constraint name is logged but
// never sent to the client.
type Error struct {
	// Kind is ErrNotFound, ErrDuplicate, ErrForeignKey, ErrConstraint,
	// ErrRetryable or ErrSchema.
	Kind error
	// Constraint is the constraint or key the database named, if any.
	Constraint string
	// Err is the driver's error.
	Err error
}

func (e *Error) Error() string {
	msg := "sql: " + e.Kind.Error()
	if e.Constraint != "" {
		msg += fmt.Sprintf(" (%s)", e.Constraint)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	if e.Kind == ErrSchema {
		msg += " (" + schemaHint + ")"
	}
	return msg
}

// Unwrap returns the kind and the driver error.
func (e *Error) Unwrap() []error {
	if e.Err == nil {
		return []error{e.Kind}
	}
	return []error{e.Kind, e.Err}
}

// HTTPError makes Dygo's error writer answer with a fitting status instead
// of 500: 404 for ErrNotFound, 409 for ErrDuplicate and ErrForeignKey, 422 for
// ErrConstraint. A retryable or schema error stays a 500.
func (e *Error) HTTPError() *errs.HTTPError {
	switch e.Kind {
	case ErrNotFound:
		return errs.NotFound("not found", errs.Code("NOT_FOUND"), errs.Cause(e))
	case ErrDuplicate:
		return errs.Conflict("already exists", errs.Code("DUPLICATE"), errs.Cause(e))
	case ErrForeignKey:
		return errs.Conflict("conflicts with a related resource", errs.Code("FOREIGN_KEY"), errs.Cause(e))
	case ErrConstraint:
		return errs.UnprocessableEntity("violates a constraint", errs.Code("CONSTRAINT"), errs.Cause(e))
	}
	return errs.Internal("", errs.Cause(e))
}

// MapError turns a driver error into an [*Error] when the dialect recognises
// it, and returns any other error unchanged. sql.ErrNoRows becomes
// ErrNotFound. Query methods call it for you; call it yourself on errors from
// [DB.SQL].
func (db *DB) MapError(err error) error { return MapError(db.dialect, err) }

// MapError is [DB.MapError] for a dialect.
func MapError(d Dialect, err error) error {
	if err == nil {
		return nil
	}
	var mapped *Error
	if errors.As(err, &mapped) {
		return err
	}
	if errors.Is(err, stdsql.ErrNoRows) {
		return &Error{Kind: ErrNotFound, Err: err}
	}
	if d == nil {
		return err
	}
	kind, constraint := d.Classify(err)
	if s := kind.sentinel(); s != nil {
		return &Error{Kind: s, Constraint: constraint, Err: err}
	}
	return err
}
