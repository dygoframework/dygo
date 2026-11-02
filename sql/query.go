package sql

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"reflect"
)

// Aliases for database/sql types, so most files need only this package.
type (
	// Result is the result of [DB.Exec].
	Result = stdsql.Result
	// Rows is the result of [DB.Query]. Close it when done.
	Rows = stdsql.Rows
	// NullString is a string that may be NULL.
	NullString = stdsql.NullString
	// NullInt64 is an int64 that may be NULL.
	NullInt64 = stdsql.NullInt64
	// NullInt32 is an int32 that may be NULL.
	NullInt32 = stdsql.NullInt32
	// NullFloat64 is a float64 that may be NULL.
	NullFloat64 = stdsql.NullFloat64
	// NullBool is a bool that may be NULL.
	NullBool = stdsql.NullBool
	// NullTime is a time.Time that may be NULL.
	NullTime = stdsql.NullTime
	// Null is any value that may be NULL.
	Null[T any] = stdsql.Null[T]
	// IsolationLevel is a transaction isolation level.
	IsolationLevel = stdsql.IsolationLevel
)

// Isolation levels for [TxOptions].
const (
	LevelDefault         = stdsql.LevelDefault
	LevelReadCommitted   = stdsql.LevelReadCommitted
	LevelRepeatableRead  = stdsql.LevelRepeatableRead
	LevelSerializable    = stdsql.LevelSerializable
	LevelReadUncommitted = stdsql.LevelReadUncommitted
)

// querier is what *database/sql.DB and *database/sql.Tx have in common.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (stdsql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*stdsql.Rows, error)
}

// conn picks the transaction in ctx for this pool, or the pool itself.
func (db *DB) conn(ctx context.Context, q string) (querier, error) {
	if db.mem != nil {
		return nil, ErrMemory
	}
	st := txFrom(ctx, db)
	if st == nil {
		return db.db, nil
	}
	if st.root.done.Load() {
		return nil, fmt.Errorf("%w: %s", ErrTxDone, q)
	}
	return st.root.tx, nil
}

// Exec runs a statement that returns no rows, such as INSERT, UPDATE or
// DELETE. Write placeholders as ?. Inside [DB.Tx] it runs in the transaction.
func (db *DB) Exec(ctx context.Context, query string, args ...any) (Result, error) {
	q, args, err := db.bind(query, args)
	if err != nil {
		return nil, err
	}
	return db.execNative(ctx, q, args)
}

// execNative runs q, already in the dialect's placeholders.
func (db *DB) execNative(ctx context.Context, q string, args []any) (Result, error) {
	c, err := db.conn(ctx, q)
	if err != nil {
		return nil, err
	}
	res, err := c.ExecContext(ctx, q, args...)
	return res, db.mapErr(err)
}

// Query runs a query and returns its rows, for streaming large results. Close
// the rows when done. Most code wants [DB.Get] or [DB.Select] instead.
func (db *DB) Query(ctx context.Context, query string, args ...any) (*Rows, error) {
	q, args, err := db.bind(query, args)
	if err != nil {
		return nil, err
	}
	return db.queryNative(ctx, q, args)
}

// queryNative runs q, already in the dialect's placeholders.
func (db *DB) queryNative(ctx context.Context, q string, args []any) (*Rows, error) {
	c, err := db.conn(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := c.QueryContext(ctx, q, args...)
	return rows, db.mapErr(err)
}

// Get runs a query and scans its first row into dest, a pointer to a struct
// (columns matched to db tags, or to the snake_case field name) or to a single
// value (int64, string, time.Time, any sql.Scanner) for a one column query.
// No row is [ErrNotFound], which answers 404.
//
//	var u User
//	err := db.Get(ctx, &u, "SELECT id, email FROM users WHERE id = ?", id)
func (db *DB) Get(ctx context.Context, dest any, query string, args ...any) error {
	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return fmt.Errorf("sql.Get: dest must be a non-nil pointer, got %T", dest)
	}
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	s, err := newRowScanner(rows, v.Elem().Type())
	if err != nil {
		return err
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return db.mapErr(err)
		}
		return db.mapErr(stdsql.ErrNoRows)
	}
	if err := s.scan(rows, v.Elem()); err != nil {
		return db.mapErr(err)
	}
	return db.mapErr(rows.Close())
}

// Select runs a query and scans every row into dest, a pointer to a slice of
// structs, of pointers to structs, or of single values. The slice is reset
// first; no rows leaves it empty, not nil-error.
//
//	var users []User
//	err := db.Select(ctx, &users, "SELECT * FROM users WHERE active = ? ORDER BY id", true)
func (db *DB) Select(ctx context.Context, dest any, query string, args ...any) error {
	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Slice {
		return fmt.Errorf("sql.Select: dest must be a pointer to a slice, got %T", dest)
	}
	slice := v.Elem()
	elem := slice.Type().Elem()
	ptr := elem.Kind() == reflect.Pointer
	base := elem
	if ptr {
		base = elem.Elem()
	}
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	s, err := newRowScanner(rows, base)
	if err != nil {
		return err
	}
	slice.SetLen(0)
	for rows.Next() {
		item := reflect.New(base)
		if err := s.scan(rows, item.Elem()); err != nil {
			return db.mapErr(err)
		}
		if ptr {
			slice.Set(reflect.Append(slice, item))
		} else {
			slice.Set(reflect.Append(slice, item.Elem()))
		}
	}
	if err := rows.Err(); err != nil {
		return db.mapErr(err)
	}
	if slice.IsNil() {
		slice.Set(reflect.MakeSlice(slice.Type(), 0, 0))
	}
	return db.mapErr(rows.Close())
}

func (db *DB) mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, stdsql.ErrTxDone) {
		return fmt.Errorf("%w: %w", ErrTxDone, err)
	}
	return db.MapError(err)
}
