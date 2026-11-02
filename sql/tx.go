package sql

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/dygoframework/dygo"
)

// ErrTxDone means a query ran with the context of a transaction that has
// already committed or rolled back, usually from a goroutine or async
// listener started inside [DB.Tx]. Pass [Detach](ctx) to such work.
var ErrTxDone = errors.New("sql: transaction already finished")

// TxOptions configure [DB.TxWith].
type TxOptions struct {
	// Isolation is the isolation level; LevelDefault uses the database's.
	Isolation IsolationLevel
	// ReadOnly starts a read only transaction.
	ReadOnly bool
	// Retries reruns the whole function up to this many times when the
	// transaction fails with [ErrRetryable] (a serialization failure or a
	// deadlock). fn must be safe to run again.
	Retries int
}

type txKey struct{}

// txState is one level of transaction for one pool: the outermost level
// owns the *database/sql.Tx, nested levels are savepoints in it.
type txState struct {
	db     *DB
	root   *txRoot
	parent *txState // the enclosing transaction, possibly of another pool
}

type txRoot struct {
	tx    *stdsql.Tx
	done  atomic.Bool
	saves atomic.Int32
}

func txFrom(ctx context.Context, db *DB) *txState {
	st, _ := ctx.Value(txKey{}).(*txState)
	for ; st != nil; st = st.parent {
		if st.db == db {
			return st
		}
	}
	return nil
}

// InTx reports whether ctx carries a transaction of any pool.
func InTx(ctx context.Context) bool { return ctx.Value(txKey{}) != nil }

// Detach returns ctx without its transactions, keeping its other values and
// its cancellation. Use it for work that may outlive the transaction, such as
// a goroutine started inside [DB.Tx], so its queries run on the pool instead
// of failing with [ErrTxDone].
func Detach(ctx context.Context) context.Context {
	if !InTx(ctx) {
		return ctx
	}
	return detached{ctx}
}

type detached struct{ context.Context }

func (d detached) Value(key any) any {
	if _, ok := key.(txKey); ok {
		return nil
	}
	return d.Context.Value(key)
}

// Tx runs fn in a transaction: it commits when fn returns nil and rolls back
// when fn returns an error or panics (the panic goes on after the rollback).
// Every query on this pool that gets fn's ctx, directly or through a
// repository, runs in the transaction:
//
//	err := db.Tx(ctx, func(ctx context.Context) error {
//		if _, err := db.Exec(ctx, "UPDATE stock SET qty = qty - ? WHERE id = ?", n, id); err != nil {
//			return err
//		}
//		_, err := db.Exec(ctx, "INSERT INTO orders (product_id, qty) VALUES (?, ?)", id, n)
//		return err
//	})
//
// Called inside another Tx on the same pool, it becomes a savepoint: an error
// rolls back only fn's work, and the outer transaction goes on.
func (db *DB) Tx(ctx context.Context, fn func(ctx context.Context) error) error {
	return db.TxWith(ctx, TxOptions{}, fn)
}

// TxWith is [DB.Tx] with options. Inside an existing transaction the options
// are ignored, since a savepoint has the outer transaction's settings.
func (db *DB) TxWith(ctx context.Context, opts TxOptions, fn func(ctx context.Context) error) error {
	if db.mem != nil {
		return fn(ctx) // memory has no transactions
	}
	if outer := txFrom(ctx, db); outer != nil {
		if outer.root.done.Load() {
			return fmt.Errorf("%w: cannot open a savepoint", ErrTxDone)
		}
		return db.savepoint(ctx, outer, fn)
	}
	var err error
	for attempt := 0; ; attempt++ {
		err = db.runTx(ctx, opts, fn)
		if err == nil || attempt >= opts.Retries || !errors.Is(err, ErrRetryable) || ctx.Err() != nil {
			return err
		}
	}
}

func (db *DB) runTx(ctx context.Context, opts TxOptions, fn func(ctx context.Context) error) (err error) {
	tx, err := db.db.BeginTx(ctx, &stdsql.TxOptions{Isolation: opts.Isolation, ReadOnly: opts.ReadOnly})
	if err != nil {
		return db.mapErr(err)
	}
	root := &txRoot{tx: tx}
	parent, _ := ctx.Value(txKey{}).(*txState)
	txCtx := context.WithValue(ctx, txKey{}, &txState{db: db, root: root, parent: parent})

	defer func() {
		if p := recover(); p != nil {
			root.done.Store(true)
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(txCtx); err != nil {
		root.done.Store(true)
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, stdsql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("sql: rollback: %w", rbErr))
		}
		return err
	}
	root.done.Store(true)
	return db.mapErr(tx.Commit())
}

func (db *DB) savepoint(ctx context.Context, outer *txState, fn func(ctx context.Context) error) (err error) {
	name := fmt.Sprintf("dygo_sp_%d", outer.root.saves.Add(1))
	if _, err := outer.root.tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return db.mapErr(err)
	}
	defer func() {
		if p := recover(); p != nil {
			_, _ = outer.root.tx.ExecContext(context.WithoutCancel(ctx), "ROLLBACK TO SAVEPOINT "+name)
			panic(p)
		}
	}()
	if err := fn(ctx); err != nil {
		if _, rbErr := outer.root.tx.ExecContext(context.WithoutCancel(ctx), "ROLLBACK TO SAVEPOINT "+name); rbErr != nil {
			return errors.Join(err, fmt.Errorf("sql: rollback to savepoint: %w", db.mapErr(rbErr)))
		}
		return err
	}
	_, err = outer.root.tx.ExecContext(ctx, "RELEASE SAVEPOINT "+name)
	return db.mapErr(err)
}

// Transactional is a route option that runs the whole handler, including
// interceptors after it and the pipes, in one transaction. The handler's ctx
// carries the transaction; an error or panic rolls it back.
//
//	r.Post("/orders", dygo.Handle(c.Create), c.db.Transactional())
func (db *DB) Transactional(opts ...TxOptions) dygo.RouteOption {
	var o TxOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	return dygo.Interceptors(dygo.InterceptorFunc(func(ec dygo.ExecutionContext, next dygo.CallHandler) (any, error) {
		var out any
		base := ec.Context()
		err := db.TxWith(base, o, func(ctx context.Context) error {
			ec.SetContext(ctx)
			var err error
			out, err = next.Handle()
			return err
		})
		ec.SetContext(base)
		return out, err
	}))
}
