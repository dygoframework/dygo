package sql

import (
	"cmp"
	stdsql "database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dygoframework/dygo"
)

// MemoryURL is the DATABASE_URL that keeps the data in memory instead of a
// database, the default in an app made by dygo new. The data is lost when
// the app stops. Change DATABASE_URL to a Postgres or MySQL URL to store it
// in that database; the code stays the same.
//
// In memory, [Repository] keeps a table per name, and [DB.Tx] runs its
// function without a transaction. Raw SQL ([DB.Get], [DB.Exec], ...) and
// [Where] clauses need a real database and fail with [ErrMemory].
const MemoryURL = "memory://"

// ErrMemory is the error of SQL run on a [MemoryURL] database.
var ErrMemory = errors.New("sql: DATABASE_URL is memory://, which runs no SQL; set a Postgres or MySQL URL in .env")

// IsMemory reports whether url is [MemoryURL].
func IsMemory(url string) bool { return strings.TrimSpace(url) == MemoryURL }

// Memory returns an empty in-memory database, for tests:
//
//	users, _ := sql.NewRepository[User](sql.Memory(), "users")
func Memory() *DB { return memoryDB("db") }

// MemoryModule is [Module] with an empty in-memory database whatever .env
// says, for tests of a feature module:
//
//	dygotest.New(&dygo.Module{Name: "test", Imports: []*dygo.Module{sql.MemoryModule(), users.Module}})
func MemoryModule() *dygo.Module {
	return Module(nil, WithConfig(Config{URL: MemoryURL}), NoHealth())
}

func memoryDB(name string) *DB {
	return &DB{dialect: memoryDialect{}, name: name, mem: &memStore{tables: map[string]*memTable{}}}
}

// memoryDialect lets a repository be built on a memory database. Its SQL is
// never run.
type memoryDialect struct{}

func (memoryDialect) Name() string                  { return "memory" }
func (memoryDialect) DriverName() string            { return "" }
func (memoryDialect) DSN(string) (string, error)    { return "", ErrMemory }
func (memoryDialect) Classify(error) (Kind, string) { return KindOther, "" }
func (memoryDialect) Placeholder(int) string        { return "?" }
func (memoryDialect) BackslashEscapes() bool        { return false }
func (memoryDialect) Quote(s string) string         { return `"` + s + `"` }
func (memoryDialect) Returning() bool               { return false }

type memStore struct {
	mu     sync.Mutex
	tables map[string]*memTable
}

func (s *memStore) table(name string) *memTable {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tables[name]
	if t == nil {
		t = &memTable{rows: map[any]any{}}
		s.tables[name] = t
	}
	return t
}

// memTable is one table: rows by primary key, in insertion order.
type memTable struct {
	mu   sync.RWMutex
	rows map[any]any // key -> T
	keys []any
	last int64 // the last auto id
}

// memKey makes 1, int64(1) and uint(1) the same key.
func memKey(id any) any {
	v := reflect.ValueOf(id)
	switch {
	case !v.IsValid():
		return nil
	case v.CanInt():
		return v.Int()
	case v.CanUint() && v.Uint() <= 1<<63-1:
		return int64(v.Uint()) //nolint:gosec // G115: checked on the line above
	}
	return id
}

func (r *Repository[T]) notFound() error { return r.db.mapErr(stdsql.ErrNoRows) }

func (r *Repository[T]) memRows(c clauses, full bool) ([]T, error) {
	if len(c.where) > 0 {
		return nil, fmt.Errorf("%w (sql.Where on %s)", ErrMemory, r.table)
	}
	t := r.mem
	t.mu.RLock()
	out := make([]T, 0, len(t.keys))
	for _, k := range t.keys {
		out = append(out, t.rows[k].(T))
	}
	t.mu.RUnlock()
	if !full {
		return out, nil
	}
	type term struct {
		path []int
		desc bool
	}
	var terms []term
	for _, o := range c.order {
		for s := range strings.SplitSeq(o, ",") {
			col, dir, err := r.orderTerm(s)
			if err != nil {
				return nil, err
			}
			terms = append(terms, term{r.byName[col].path, dir == " DESC"})
		}
	}
	if len(terms) > 0 {
		slices.SortStableFunc(out, func(a, b T) int {
			va, vb := reflect.ValueOf(&a).Elem(), reflect.ValueOf(&b).Elem()
			for _, tm := range terms {
				n := compareValues(va.FieldByIndex(tm.path), vb.FieldByIndex(tm.path))
				if tm.desc {
					n = -n
				}
				if n != 0 {
					return n
				}
			}
			return 0
		})
	}
	if c.limit < 0 || c.offset < 0 {
		return nil, fmt.Errorf("sql: negative Limit or Offset")
	}
	out = out[min(c.offset, len(out)):]
	if c.limit > 0 {
		out = out[:min(c.limit, len(out))]
	}
	return out, nil
}

// compareValues orders two values of a column; nil pointers come first.
func compareValues(a, b reflect.Value) int {
	if a.Kind() == reflect.Pointer {
		switch {
		case a.IsNil() && b.IsNil():
			return 0
		case a.IsNil():
			return -1
		case b.IsNil():
			return 1
		}
		a, b = a.Elem(), b.Elem()
	}
	if ta, ok := a.Interface().(time.Time); ok {
		return ta.Compare(b.Interface().(time.Time))
	}
	switch {
	case a.CanInt():
		return cmp.Compare(a.Int(), b.Int())
	case a.CanUint():
		return cmp.Compare(a.Uint(), b.Uint())
	case a.CanFloat():
		return cmp.Compare(a.Float(), b.Float())
	case a.Kind() == reflect.String:
		return cmp.Compare(a.String(), b.String())
	case a.Kind() == reflect.Bool:
		return cmp.Compare(boolInt(a.Bool()), boolInt(b.Bool()))
	}
	return 0
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (r *Repository[T]) memCreate(v *T) error {
	rv := reflect.ValueOf(v).Elem()
	r.stamp(rv, now(), true)
	t := r.mem
	t.mu.Lock()
	defer t.mu.Unlock()
	pk := rv.FieldByIndex(r.pk.path)
	if r.pk.auto {
		t.last++
		if pk.CanInt() {
			pk.SetInt(t.last)
		} else {
			pk.SetUint(uint64(t.last)) //nolint:gosec // G115: counts up from 1
		}
	}
	key := memKey(pk.Interface())
	if _, ok := t.rows[key]; ok {
		return &Error{Kind: ErrDuplicate, Constraint: r.table + "_pkey", Err: fmt.Errorf("duplicate %s %v in %s", r.pk.name, key, r.table)}
	}
	if n, ok := key.(int64); ok && n > t.last {
		t.last = n
	}
	t.rows[key] = *v
	t.keys = append(t.keys, key)
	return nil
}

func (r *Repository[T]) memUpdate(v *T) error {
	rv := reflect.ValueOf(v).Elem()
	key := memKey(rv.FieldByIndex(r.pk.path).Interface())
	t := r.mem
	t.mu.Lock()
	defer t.mu.Unlock()
	old, ok := t.rows[key]
	if !ok {
		return r.notFound()
	}
	r.stamp(rv, now(), false)
	row := *v
	ov, nv := reflect.ValueOf(old.(T)), reflect.ValueOf(&row).Elem()
	for _, c := range r.cols { // Update leaves these columns as they are
		if c.auto || c.created {
			nv.FieldByIndex(c.path).Set(ov.FieldByIndex(c.path))
		}
	}
	t.rows[key] = row
	return nil
}

func (r *Repository[T]) memPatch(id any, set Set) error {
	key := memKey(id)
	t := r.mem
	t.mu.Lock()
	defer t.mu.Unlock()
	old, ok := t.rows[key]
	if !ok {
		return r.notFound()
	}
	row := old.(T)
	rv := reflect.ValueOf(&row).Elem()
	for k, val := range set {
		f := rv.FieldByIndex(r.byName[k].path)
		x := reflect.ValueOf(val)
		switch {
		case !x.IsValid():
			f.SetZero()
		case x.Type().AssignableTo(f.Type()):
			f.Set(x)
		case x.Type().ConvertibleTo(f.Type()):
			f.Set(x.Convert(f.Type()))
		case f.Kind() == reflect.Pointer && x.Type().ConvertibleTo(f.Type().Elem()):
			p := reflect.New(f.Type().Elem())
			p.Elem().Set(x.Convert(f.Type().Elem()))
			f.Set(p)
		default:
			return fmt.Errorf("sql: cannot set %s.%s (%s) to a %T", r.table, k, f.Type(), val)
		}
	}
	t2 := now()
	for _, c := range r.updated {
		if _, ok := set[c.name]; !ok {
			rv.FieldByIndex(c.path).Set(reflect.ValueOf(t2))
		}
	}
	t.rows[key] = row
	return nil
}

func (r *Repository[T]) memDelete(id any) error {
	key := memKey(id)
	t := r.mem
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.rows[key]; !ok {
		return r.notFound()
	}
	delete(t.rows, key)
	t.keys = slices.DeleteFunc(t.keys, func(k any) bool { return k == key })
	return nil
}

func (r *Repository[T]) memFind(id any) (T, error) {
	t := r.mem
	t.mu.RLock()
	defer t.mu.RUnlock()
	v, ok := t.rows[memKey(id)]
	if !ok {
		var zero T
		return zero, r.notFound()
	}
	return v.(T), nil
}
