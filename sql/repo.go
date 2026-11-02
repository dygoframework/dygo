package sql

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/errs"
)

// Repository gives CRUD methods for one table, mapped to the struct T by its
// db tags. Get one per entity with [ForFeature] in the feature module and
// inject *sql.Repository[T].
//
// Tag options after the column name:
//
//	ID        int64     `db:"id,pk,auto"`         // primary key, set by the database
//	CreatedAt time.Time `db:"created_at,created"` // set by Create when zero
//	UpdatedAt time.Time `db:"updated_at,updated"` // set by Create, Update and Patch
//	Note      string    `db:"-"`                  // not a column
//
// T needs exactly one pk column. auto columns are left out of INSERT and
// read back after it: with RETURNING on Postgres, from LastInsertId on MySQL
// (which allows one integer auto column). Every method runs in the
// transaction in ctx, if any.
//
// The SQL for each method is built once, when the repository is created.
// Anything the repository cannot express is a custom query on the same struct
// through [Repository.DB].
type Repository[T any] struct {
	db    *DB
	mem   *memTable // the table of a MemoryURL database, else nil
	table string    // as given, for errors
	qt    string    // quoted

	cols    []column
	byName  map[string]column
	pk      column
	auto    []column // read back after INSERT
	ins     []column // written by INSERT
	upd     []column // written by Update: not pk, auto or created
	created []column
	updated []column

	sel       string // SELECT a, b FROM t
	find      string // sel WHERE pk = ?
	insert    string // INSERT INTO t (a, b) VALUES
	insertRow string // (?, ?) in the dialect's placeholders, for one row
	returning string // " RETURNING x" or ""
	update    string // UPDATE t SET a = ?, b = ? WHERE pk = ?
	del       string // DELETE FROM t WHERE pk = ?
	count     string // SELECT COUNT(*) FROM t
	exists    string // SELECT 1 FROM t
}

// NewRepository builds the repository for T on table. [ForFeature] calls it
// at boot, so a tag error stops the app before it listens.
func NewRepository[T any](db *DB, table string) (*Repository[T], error) {
	t := reflect.TypeFor[T]()
	if db == nil {
		return nil, fmt.Errorf("sql.Repository[%s]: db is nil", t)
	}
	if !isStructTarget(t) {
		return nil, fmt.Errorf("sql.Repository[%s]: T must be a struct", t)
	}
	if table == "" {
		return nil, fmt.Errorf("sql.Repository[%s]: table name is empty", t)
	}
	fm := fieldsOf(t)
	if fm.err != nil {
		return nil, fmt.Errorf("sql.Repository[%s]: %w", t, fm.err)
	}
	d := db.dialect
	r := &Repository[T]{db: db, table: table, qt: d.Quote(table), cols: fm.cols, byName: map[string]column{}}
	var pks []column
	for _, c := range fm.cols {
		r.byName[c.name] = c
		if (c.created || c.updated) && c.typ != timeType {
			return nil, fmt.Errorf("sql.Repository[%s]: column %q is created or updated, so it must be a time.Time, not %s", t, c.name, c.typ)
		}
		if c.pk {
			pks = append(pks, c)
		}
		if c.auto {
			r.auto = append(r.auto, c)
		} else {
			r.ins = append(r.ins, c)
		}
		if !c.pk && !c.auto && !c.created {
			r.upd = append(r.upd, c)
		}
		if c.created {
			r.created = append(r.created, c)
		}
		if c.updated {
			r.updated = append(r.updated, c)
		}
	}
	if len(pks) != 1 {
		return nil, fmt.Errorf("sql.Repository[%s]: need exactly one primary key column (db:\"id,pk\"), found %d; use custom queries for composite keys", t, len(pks))
	}
	r.pk = pks[0]
	if len(r.ins) == 0 {
		return nil, fmt.Errorf("sql.Repository[%s]: every column is auto, so INSERT has nothing to write", t)
	}
	if !d.Returning() && len(r.auto) > 0 {
		if len(r.auto) > 1 {
			return nil, fmt.Errorf("sql.Repository[%s]: %s can read back one auto column (the auto increment), found %d", t, d.Name(), len(r.auto))
		}
		if !isInteger(r.auto[0].typ) {
			return nil, fmt.Errorf("sql.Repository[%s]: auto column %q must be an integer on %s, which reads it from LastInsertId", t, r.auto[0].name, d.Name())
		}
	}

	names := func(cs []column) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = d.Quote(c.name)
		}
		return out
	}
	pkWhere := " WHERE " + d.Quote(r.pk.name) + " = ?"
	r.sel = "SELECT " + strings.Join(names(r.cols), ", ") + " FROM " + r.qt
	r.find = r.sel + pkWhere
	r.insert = "INSERT INTO " + r.qt + " (" + strings.Join(names(r.ins), ", ") + ") VALUES "
	r.insertRow = "(" + strings.Repeat("?, ", len(r.ins)-1) + "?)"
	if d.Returning() && len(r.auto) > 0 {
		r.returning = " RETURNING " + strings.Join(names(r.auto), ", ")
	}
	if len(r.upd) > 0 {
		set := names(r.upd)
		for i := range set {
			set[i] += " = ?"
		}
		r.update = "UPDATE " + r.qt + " SET " + strings.Join(set, ", ") + pkWhere
	}
	r.del = "DELETE FROM " + r.qt + pkWhere
	r.count = "SELECT COUNT(*) FROM " + r.qt
	r.exists = "SELECT 1 FROM " + r.qt
	if db.mem != nil {
		r.mem = db.mem.table(table)
	}
	return r, nil
}

func isInteger(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

// FeatureOption configures [ForFeature].
type FeatureOption func(*featureOptions)

type featureOptions struct{ db string }

// Using picks the pool provided with sql.Named(name) instead of the default
// *sql.DB.
func Using(name string) FeatureOption { return func(o *featureOptions) { o.db = name } }

// ForFeature provides *sql.Repository[T] for table. Put it in the feature
// module's providers:
//
//	Providers: []dygo.Provider{sql.ForFeature[User]("users"), dygo.Provide(NewService)}
//
//	func NewService(users *sql.Repository[User]) *Service
func ForFeature[T any](table string, opts ...FeatureOption) dygo.Provider {
	var o featureOptions
	for _, fn := range opts {
		fn(&o)
	}
	if o.db == "" {
		return dygo.Provide(func(db *DB) (*Repository[T], error) { return NewRepository[T](db, table) })
	}
	// The pool is the named token Token(o.db). A constructor parameter can
	// name a token only through a dygo.In struct field tag, so build that
	// struct type, and a constructor taking it, for this name.
	in := reflect.StructOf([]reflect.StructField{
		{Name: "In", Type: reflect.TypeFor[dygo.In](), Anonymous: true},
		{Name: "DB", Type: reflect.TypeFor[*DB](), Tag: reflect.StructTag(`name:"` + o.db + `"`)},
	})
	errType := reflect.TypeFor[error]()
	ft := reflect.FuncOf([]reflect.Type{in}, []reflect.Type{reflect.TypeFor[*Repository[T]](), errType}, false)
	ctor := reflect.MakeFunc(ft, func(args []reflect.Value) []reflect.Value {
		db, _ := args[0].Field(1).Interface().(*DB)
		r, err := NewRepository[T](db, table)
		ev := reflect.Zero(errType)
		if err != nil {
			ev = reflect.ValueOf(&err).Elem()
		}
		return []reflect.Value{reflect.ValueOf(r), ev}
	})
	return dygo.Provide(ctor.Interface())
}

// DB returns the pool, for custom queries on the same struct:
//
//	var out []User
//	err := users.DB().Select(ctx, &out, "SELECT u.* FROM users u JOIN teams t ON ...")
func (r *Repository[T]) DB() *DB { return r.db }

// Table returns the table name.
func (r *Repository[T]) Table() string { return r.table }

// Find returns the row with primary key id, or [ErrNotFound] (404).
func (r *Repository[T]) Find(ctx context.Context, id any) (T, error) {
	if r.mem != nil {
		return r.memFind(id)
	}
	var v T
	err := r.db.Get(ctx, &v, r.find, id)
	return v, err
}

// FindBy returns the first row matching the clauses, or [ErrNotFound]:
//
//	u, err := users.FindBy(ctx, sql.Where("email = ?", email))
func (r *Repository[T]) FindBy(ctx context.Context, clauses ...Clause) (T, error) {
	var v T
	c := gather(clauses)
	if c.limit == 0 {
		c.limit = 1
	}
	if r.mem != nil {
		rows, err := r.memRows(c, true)
		if err == nil && len(rows) == 0 {
			err = r.notFound()
		}
		if err != nil {
			return v, err
		}
		return rows[0], nil
	}
	q, args, err := r.render(r.sel, c, true)
	if err != nil {
		return v, err
	}
	err = r.db.Get(ctx, &v, q, args...)
	return v, err
}

// List returns the rows matching the clauses, in their order, never nil:
//
//	users, err := repo.List(ctx, sql.Where("active = ?", true), sql.OrderBy("created_at DESC"), sql.Limit(20))
func (r *Repository[T]) List(ctx context.Context, clauses ...Clause) ([]T, error) {
	if r.mem != nil {
		return r.memRows(gather(clauses), true)
	}
	q, args, err := r.render(r.sel, gather(clauses), true)
	if err != nil {
		return nil, err
	}
	var out []T
	if err := r.db.Select(ctx, &out, q, args...); err != nil {
		return nil, err
	}
	return out, nil
}

// Count returns the number of rows matching the Where clauses. OrderBy, Limit
// and Offset are ignored.
func (r *Repository[T]) Count(ctx context.Context, clauses ...Clause) (int64, error) {
	if r.mem != nil {
		rows, err := r.memRows(gather(clauses), false)
		return int64(len(rows)), err
	}
	q, args, err := r.render(r.count, gather(clauses), false)
	if err != nil {
		return 0, err
	}
	var n int64
	err = r.db.Get(ctx, &n, q, args...)
	return n, err
}

// Exists reports whether a row matches the Where clauses.
func (r *Repository[T]) Exists(ctx context.Context, clauses ...Clause) (bool, error) {
	c := gather(clauses)
	c.order, c.offset, c.limit = nil, 0, 1
	if r.mem != nil {
		rows, err := r.memRows(c, false)
		return len(rows) > 0, err
	}
	q, args, err := r.render(r.exists, c, true)
	if err != nil {
		return false, err
	}
	var one int
	err = r.db.Get(ctx, &one, q, args...)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Page is one page of a listing, as [Repository.Page] returns it.
type Page[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Size  int   `json:"size"`
}

// Pages is the number of pages for Total rows.
func (p Page[T]) Pages() int {
	if p.Size <= 0 {
		return 0
	}
	return int((p.Total + int64(p.Size) - 1) / int64(p.Size))
}

// Page returns page number page (from 1) of size rows matching the clauses,
// with the total count. A page below 1 is page 1. Limit and Offset clauses
// are replaced by the page's own.
func (r *Repository[T]) Page(ctx context.Context, page, size int, clauses ...Clause) (Page[T], error) {
	if size < 1 {
		return Page[T]{}, fmt.Errorf("sql: page size must be at least 1, got %d", size)
	}
	page = max(page, 1)
	total, err := r.Count(ctx, clauses...)
	if err != nil {
		return Page[T]{}, err
	}
	c := gather(clauses)
	c.limit, c.offset = size, (page-1)*size
	var items []T
	if r.mem != nil {
		if items, err = r.memRows(c, true); err != nil {
			return Page[T]{}, err
		}
		return Page[T]{Items: items, Total: total, Page: page, Size: size}, nil
	}
	q, args, err := r.render(r.sel, c, true)
	if err != nil {
		return Page[T]{}, err
	}
	if err := r.db.Select(ctx, &items, q, args...); err != nil {
		return Page[T]{}, err
	}
	return Page[T]{Items: items, Total: total, Page: page, Size: size}, nil
}

// now is the time created and updated columns get: UTC, to the microsecond,
// which both databases store (use DATETIME(6) or TIMESTAMP(6) on MySQL).
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func (r *Repository[T]) stamp(v reflect.Value, t time.Time, insert bool) {
	if insert {
		for _, c := range r.created {
			if f := v.FieldByIndex(c.path); f.Interface().(time.Time).IsZero() {
				f.Set(reflect.ValueOf(t))
			}
		}
	}
	for _, c := range r.updated {
		v.FieldByIndex(c.path).Set(reflect.ValueOf(t))
	}
}

func (r *Repository[T]) insertArgs(v reflect.Value, into []any) []any {
	for _, c := range r.ins {
		into = append(into, v.FieldByIndex(c.path).Interface())
	}
	return into
}

// Create inserts v and sets its auto columns from the database, and its
// created and updated columns to now. Values in auto columns are not written.
// A unique violation is [ErrDuplicate] (409).
func (r *Repository[T]) Create(ctx context.Context, v *T) error {
	if v == nil {
		return errors.New("sql: Create with a nil value")
	}
	if r.mem != nil {
		return r.memCreate(v)
	}
	rv := reflect.ValueOf(v).Elem()
	r.stamp(rv, now(), true)
	args := r.insertArgs(rv, make([]any, 0, len(r.ins)))
	q := r.insert + r.insertRow
	if r.returning != "" {
		rows, err := r.db.Query(ctx, q+r.returning, args...)
		if err != nil {
			return err
		}
		return r.readReturning(rows, func(int) reflect.Value { return rv }, 1)
	}
	res, err := r.db.Exec(ctx, q, args...)
	if err != nil {
		return err
	}
	return r.setLastID(rv, res)
}

func (r *Repository[T]) setLastID(rv reflect.Value, res Result) error {
	if len(r.auto) == 0 {
		return nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("sql: reading the inserted id: %w", err)
	}
	f := rv.FieldByIndex(r.auto[0].path)
	if f.CanInt() {
		f.SetInt(id)
	} else {
		f.SetUint(uint64(id)) //nolint:gosec // G115: auto increment ids are positive
	}
	return nil
}

// readReturning scans the RETURNING rows into the auto columns of item(i).
func (r *Repository[T]) readReturning(rows *Rows, item func(i int) reflect.Value, n int) error {
	defer func() { _ = rows.Close() }()
	ptrs := make([]any, len(r.auto))
	i := 0
	for rows.Next() {
		if i >= n {
			return fmt.Errorf("sql: INSERT into %s returned more than %d rows", r.table, n)
		}
		v := item(i)
		for j, c := range r.auto {
			ptrs[j] = v.FieldByIndex(c.path).Addr().Interface()
		}
		if err := rows.Scan(ptrs...); err != nil {
			return r.db.mapErr(err)
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return r.db.mapErr(err)
	}
	if i != n {
		return fmt.Errorf("sql: INSERT into %s returned %d rows, want %d", r.table, i, n)
	}
	return r.db.mapErr(rows.Close())
}

// maxParams is the bind parameter limit per statement on both databases.
const maxParams = 65535

// maxBatch caps the rows of one multi row INSERT, to keep statements small.
const maxBatch = 1000

// CreateMany inserts items with multi row INSERTs, sets created and updated
// columns in place, and on Postgres sets the auto columns too. (On MySQL,
// read them back with a query if you need them: LastInsertId gives only the
// first.) A large slice is split into several statements, run in one
// transaction so they all commit or none do.
func (r *Repository[T]) CreateMany(ctx context.Context, items []T) error {
	if len(items) == 0 {
		return nil
	}
	if r.mem != nil {
		for i := range items {
			if err := r.memCreate(&items[i]); err != nil {
				return err
			}
		}
		return nil
	}
	per := min(maxBatch, maxParams/len(r.ins))
	t := now()
	for i := range items {
		r.stamp(reflect.ValueOf(&items[i]).Elem(), t, true)
	}
	if len(items) <= per {
		return r.insertBatch(ctx, items)
	}
	return r.db.Tx(ctx, func(ctx context.Context) error {
		for start := 0; start < len(items); start += per {
			if err := r.insertBatch(ctx, items[start:min(start+per, len(items))]); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository[T]) insertBatch(ctx context.Context, items []T) error {
	var b strings.Builder
	b.Grow(len(r.insert) + len(items)*(len(r.insertRow)+2) + len(r.returning))
	b.WriteString(r.insert)
	args := make([]any, 0, len(items)*len(r.ins))
	for i := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(r.insertRow)
		args = r.insertArgs(reflect.ValueOf(&items[i]).Elem(), args)
	}
	b.WriteString(r.returning)
	// Batch sizes vary, so these statements skip the query cache: the text is
	// rendered in the dialect's placeholders here and sent as is.
	q := r.db.native(b.String(), len(args))
	if r.returning != "" {
		rows, err := r.db.queryNative(ctx, q, args)
		if err != nil {
			return err
		}
		return r.readReturning(rows, func(i int) reflect.Value { return reflect.ValueOf(&items[i]).Elem() }, len(items))
	}
	_, err := r.db.execNative(ctx, q, args)
	return err
}

// Update writes every column of v except the primary key, auto and created
// columns, to the row with v's primary key, and sets its updated columns to
// now. No such row is [ErrNotFound] (404).
func (r *Repository[T]) Update(ctx context.Context, v *T) error {
	if v == nil {
		return errors.New("sql: Update with a nil value")
	}
	if r.update == "" {
		return fmt.Errorf("sql: %s has no columns to update", r.table)
	}
	if r.mem != nil {
		return r.memUpdate(v)
	}
	rv := reflect.ValueOf(v).Elem()
	r.stamp(rv, now(), false)
	args := make([]any, 0, len(r.upd)+1)
	for _, c := range r.upd {
		args = append(args, rv.FieldByIndex(c.path).Interface())
	}
	args = append(args, rv.FieldByIndex(r.pk.path).Interface())
	res, err := r.db.Exec(ctx, r.update, args...)
	return r.affected(res, err)
}

// Set lists the columns [Repository.Patch] writes, by column name.
type Set map[string]any

// Patch writes only the columns in set to the row with primary key id, and
// sets the updated columns to now unless set names them. Column names are
// checked against T's tags, so a name from user input cannot inject SQL; an
// unknown one is [ErrUnknownColumn] (400). No such row is [ErrNotFound].
//
//	err := users.Patch(ctx, id, sql.Set{"name": in.Name})
func (r *Repository[T]) Patch(ctx context.Context, id any, set Set) error {
	keys := make([]string, 0, len(set)+len(r.updated))
	for k := range set {
		c, ok := r.byName[k]
		if !ok || c.pk || c.auto {
			return &columnError{col: k, table: r.table}
		}
		keys = append(keys, k)
	}
	slices.Sort(keys) // the same set gives the same SQL, so it is cached once
	t := now()
	var stamped []string
	for _, c := range r.updated {
		if _, ok := set[c.name]; !ok {
			stamped = append(stamped, c.name)
		}
	}
	if len(keys)+len(stamped) == 0 {
		return errors.New("sql: Patch with no columns")
	}
	if r.mem != nil {
		return r.memPatch(id, set)
	}
	d := r.db.dialect
	var b strings.Builder
	b.WriteString("UPDATE " + r.qt + " SET ")
	args := make([]any, 0, len(keys)+len(stamped)+1)
	for i, k := range append(keys, stamped...) {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.Quote(k) + " = ?")
		if i < len(keys) {
			args = append(args, set[k])
		} else {
			args = append(args, t)
		}
	}
	b.WriteString(" WHERE " + d.Quote(r.pk.name) + " = ?")
	args = append(args, id)
	res, err := r.db.Exec(ctx, b.String(), args...)
	return r.affected(res, err)
}

// Delete deletes the row with primary key id. No such row is [ErrNotFound].
func (r *Repository[T]) Delete(ctx context.Context, id any) error {
	if r.mem != nil {
		return r.memDelete(id)
	}
	res, err := r.db.Exec(ctx, r.del, id)
	return r.affected(res, err)
}

func (r *Repository[T]) affected(res Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sql: reading rows affected: %w", err)
	}
	if n == 0 {
		return r.db.mapErr(stdsql.ErrNoRows)
	}
	return nil
}

// Clause narrows or orders the rows of [Repository.List], FindBy, Count,
// Exists and Page.
type Clause func(*clauses)

type clauses struct {
	where  []string
	args   []any
	order  []string
	limit  int
	offset int
}

func gather(cs []Clause) clauses {
	var c clauses
	for _, fn := range cs {
		fn(&c)
	}
	return c
}

// Where adds a condition, written in SQL with ? placeholders. Several Where
// clauses are joined with AND. Pass user input only as arguments, never by
// building cond with string concatenation:
//
//	sql.Where("email = ? AND deleted_at IS NULL", email)
func Where(cond string, args ...any) Clause {
	return func(c *clauses) {
		c.where = append(c.where, cond)
		c.args = append(c.args, args...)
	}
}

// OrderBy sorts by columns, each "name" or "name ASC" / "name DESC" (several
// may be given in one string, separated by commas). Names must be columns of
// T, so a sort parameter from the request is safe to pass; an unknown one is
// [ErrUnknownColumn] (400).
func OrderBy(cols ...string) Clause {
	return func(c *clauses) { c.order = append(c.order, cols...) }
}

// Limit returns at most n rows.
func Limit(n int) Clause { return func(c *clauses) { c.limit = n } }

// Offset skips the first n rows.
func Offset(n int) Clause { return func(c *clauses) { c.offset = n } }

// noLimit is the LIMIT used for an Offset without a Limit, which MySQL
// requires; both databases accept it.
const noLimit = "9223372036854775807"

// render appends the clauses to base. full adds ORDER BY, LIMIT and OFFSET.
func (r *Repository[T]) render(base string, c clauses, full bool) (string, []any, error) {
	if len(c.where) == 0 && (!full || (len(c.order) == 0 && c.limit == 0 && c.offset == 0)) {
		return base, c.args, nil
	}
	var b strings.Builder
	b.WriteString(base)
	for i, w := range c.where {
		if i == 0 {
			b.WriteString(" WHERE (")
		} else {
			b.WriteString(" AND (")
		}
		b.WriteString(w)
		b.WriteByte(')')
	}
	if !full {
		return b.String(), c.args, nil
	}
	n := 0
	for _, o := range c.order {
		for term := range strings.SplitSeq(o, ",") {
			col, dir, err := r.orderTerm(term)
			if err != nil {
				return "", nil, err
			}
			if n == 0 {
				b.WriteString(" ORDER BY ")
			} else {
				b.WriteString(", ")
			}
			b.WriteString(r.db.dialect.Quote(col) + dir)
			n++
		}
	}
	if c.limit < 0 || c.offset < 0 {
		return "", nil, fmt.Errorf("sql: negative Limit or Offset")
	}
	switch {
	case c.limit > 0:
		b.WriteString(" LIMIT " + strconv.Itoa(c.limit))
	case c.offset > 0:
		b.WriteString(" LIMIT " + noLimit)
	}
	if c.offset > 0 {
		b.WriteString(" OFFSET " + strconv.Itoa(c.offset))
	}
	return b.String(), c.args, nil
}

func (r *Repository[T]) orderTerm(term string) (col, dir string, err error) {
	f := strings.Fields(term)
	if len(f) == 0 || len(f) > 2 {
		return "", "", &columnError{col: strings.TrimSpace(term), table: r.table}
	}
	if _, ok := r.byName[f[0]]; !ok {
		return "", "", &columnError{col: f[0], table: r.table}
	}
	if len(f) == 2 {
		switch strings.ToUpper(f[1]) {
		case "ASC":
			dir = " ASC"
		case "DESC":
			dir = " DESC"
		default:
			return "", "", &columnError{col: term, table: r.table}
		}
	}
	return f[0], dir, nil
}

// ErrUnknownColumn means OrderBy or Patch named a column that T does not
// map. It answers 400.
var ErrUnknownColumn = errors.New("unknown column")

type columnError struct{ col, table string }

func (e *columnError) Error() string {
	return fmt.Sprintf("sql: unknown column %q for %s", e.col, e.table)
}

func (e *columnError) Is(target error) bool { return target == ErrUnknownColumn }

// HTTPError answers 400, naming the column, since it usually comes from a
// sort or field parameter in the request.
func (e *columnError) HTTPError() *errs.HTTPError {
	return errs.BadRequest(fmt.Sprintf("unknown column %q", e.col), errs.Code("UNKNOWN_COLUMN"), errs.Cause(e))
}
