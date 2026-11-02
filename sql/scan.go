package sql

import (
	stdsql "database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
)

// fieldMap is the column name to field index path map of a struct type,
// built once per type from its db tags.
type fieldMap struct {
	byColumn map[string][]int
	cols     []column // in field order, for repositories
	err      error
}

// column is one mapped field and the options from its db tag.
type column struct {
	name string
	path []int
	typ  reflect.Type
	// pk marks the primary key, auto a value the database sets on insert
	// (an auto increment or a default), created and updated timestamps the
	// repository sets.
	pk, auto, created, updated bool
}

var fieldMaps sync.Map // reflect.Type -> *fieldMap

var (
	scannerType = reflect.TypeFor[stdsql.Scanner]()
	timeType    = reflect.TypeFor[time.Time]()
)

// isStructTarget reports whether t is scanned field by field, as opposed to a
// single column: a struct that is not a sql.Scanner or time.Time.
func isStructTarget(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t != timeType && !reflect.PointerTo(t).Implements(scannerType)
}

func fieldsOf(t reflect.Type) *fieldMap {
	if m, ok := fieldMaps.Load(t); ok {
		return m.(*fieldMap)
	}
	m := &fieldMap{byColumn: map[string][]int{}}
	m.err = collect(t, nil, m)
	fieldMaps.Store(t, m)
	return m
}

func collect(t reflect.Type, prefix []int, into *fieldMap) error {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, hasTag := f.Tag.Lookup("db")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		path := append(append([]int(nil), prefix...), i)
		if f.Anonymous && !hasTag {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				return fmt.Errorf("sql: embedded pointer %s in %s is not supported; embed the struct by value", ft, t)
			}
			if ft.Kind() == reflect.Struct && isStructTarget(ft) {
				if err := collect(ft, path, into); err != nil {
					return err
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = snake(f.Name)
		}
		if _, dup := into.byColumn[name]; dup {
			return fmt.Errorf("sql: column %q is mapped twice in %s", name, t)
		}
		c := column{name: name, path: path, typ: f.Type}
		for _, o := range strings.Split(opts, ",") {
			switch strings.TrimSpace(o) {
			case "":
			case "pk":
				c.pk = true
			case "auto":
				c.auto = true
			case "created":
				c.created = true
			case "updated":
				c.updated = true
			default:
				return fmt.Errorf("sql: unknown option %q in the db tag of %s.%s", o, t, f.Name)
			}
		}
		into.byColumn[name] = path
		into.cols = append(into.cols, c)
	}
	return nil
}

// snake turns a Go field name into snake_case: UserID becomes user_id,
// CreatedAt becomes created_at.
func snake(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) {
			if i > 0 && (unicode.IsLower(r[i-1]) || (i+1 < len(r) && unicode.IsLower(r[i+1]) && unicode.IsUpper(r[i-1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(c))
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// plan is the field path for each result column, in column order.
type plan [][]int

type planKey struct {
	t    reflect.Type
	cols string
}

var plans sync.Map // planKey -> plan

func planFor(t reflect.Type, cols []string) (plan, error) {
	key := planKey{t, strings.Join(cols, "\x00")}
	if p, ok := plans.Load(key); ok {
		return p.(plan), nil
	}
	fm := fieldsOf(t)
	if fm.err != nil {
		return nil, fm.err
	}
	p := make(plan, len(cols))
	for i, c := range cols {
		path, ok := fm.byColumn[c]
		if !ok {
			path, ok = fm.byColumn[strings.ToLower(c)]
		}
		if !ok {
			return nil, fmt.Errorf("sql: column %q has no field in %s (add a db:%q tag or select fewer columns)", c, t, c)
		}
		p[i] = path
	}
	plans.Store(key, p)
	return p, nil
}

// rowScanner scans rows into values of one type.
type rowScanner struct {
	t      reflect.Type // the element type (struct or single column)
	plan   plan
	ptrs   []any
	single bool
}

func newRowScanner(rows *stdsql.Rows, t reflect.Type) (*rowScanner, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !isStructTarget(t) {
		if len(cols) != 1 {
			return nil, fmt.Errorf("sql: scanning into %s needs one column, the query returns %d", t, len(cols))
		}
		return &rowScanner{t: t, single: true, ptrs: make([]any, 1)}, nil
	}
	p, err := planFor(t, cols)
	if err != nil {
		return nil, err
	}
	return &rowScanner{t: t, plan: p, ptrs: make([]any, len(p))}, nil
}

// scan reads the current row into v, which is addressable and of type t.
func (s *rowScanner) scan(rows *stdsql.Rows, v reflect.Value) error {
	if s.single {
		s.ptrs[0] = v.Addr().Interface()
		return rows.Scan(s.ptrs...)
	}
	for i, path := range s.plan {
		s.ptrs[i] = v.FieldByIndex(path).Addr().Interface()
	}
	return rows.Scan(s.ptrs...)
}
