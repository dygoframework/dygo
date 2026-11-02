package sql

import (
	stdsql "database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// compiled is a query split at its ? placeholders.
type compiled struct {
	// parts has one more element than there are placeholders: the text
	// before the first, between each pair, and after the last. "??" in the
	// query is already a single "?" here.
	parts []string
	// plain is the query for the dialect when no argument is an [In] list,
	// built once.
	plain string
}

func (c *compiled) placeholders() int { return len(c.parts) - 1 }

// queryCache keeps compiled queries per query string. Apps have a bounded set
// of query strings, but a query built with fmt.Sprintf could grow it without
// end, so it stops adding once it holds maxCached entries.
type queryCache struct {
	m  sync.Map // string -> *compiled
	n  int64
	mu sync.Mutex
}

const maxCached = 4096

func (qc *queryCache) get(d Dialect, q string) *compiled {
	if c, ok := qc.m.Load(q); ok {
		return c.(*compiled)
	}
	c := compile(q, d.BackslashEscapes())
	c.plain = c.render(d, nil)
	qc.mu.Lock()
	if qc.n < maxCached {
		qc.n++
		qc.m.Store(q, c)
	}
	qc.mu.Unlock()
	return c
}

// compile splits q at every ? that is not inside a string, a quoted
// identifier or a comment. "??" stands for a literal "?" (the Postgres JSON
// operators ?, ?| and ?& are written ??, ??| and ??&).
func compile(q string, backslash bool) *compiled {
	var parts []string
	var cur strings.Builder
	n := len(q)
	for i := 0; i < n; i++ {
		c := q[i]
		switch {
		case c == '?':
			if i+1 < n && q[i+1] == '?' {
				cur.WriteByte('?')
				i++
				continue
			}
			parts = append(parts, cur.String())
			cur.Reset()
		case c == '\'':
			esc := backslash || (i > 0 && (q[i-1] == 'E' || q[i-1] == 'e') && (i < 2 || !isIdent(q[i-2])))
			j := skipQuoted(q, i, '\'', esc)
			cur.WriteString(q[i:j])
			i = j - 1
		case c == '"' || c == '`':
			j := skipQuoted(q, i, c, false)
			cur.WriteString(q[i:j])
			i = j - 1
		case c == '-' && i+1 < n && q[i+1] == '-':
			j := strings.IndexByte(q[i:], '\n')
			if j < 0 {
				j = n - i
			}
			cur.WriteString(q[i : i+j])
			i += j - 1
		case c == '#' && backslash: // MySQL line comment
			j := strings.IndexByte(q[i:], '\n')
			if j < 0 {
				j = n - i
			}
			cur.WriteString(q[i : i+j])
			i += j - 1
		case c == '/' && i+1 < n && q[i+1] == '*':
			j := strings.Index(q[i+2:], "*/")
			end := n
			if j >= 0 {
				end = i + 2 + j + 2
			}
			cur.WriteString(q[i:end])
			i = end - 1
		case c == '$' && !backslash:
			if end := dollarQuoteEnd(q, i); end > 0 {
				cur.WriteString(q[i:end])
				i = end - 1
				continue
			}
			cur.WriteByte(c)
		default:
			cur.WriteByte(c)
		}
	}
	parts = append(parts, cur.String())
	return &compiled{parts: parts}
}

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// skipQuoted returns the index just past the quoted text starting at q[i].
// A doubled quote is an escaped quote; with esc, so is a backslash. An
// unterminated quote runs to the end.
func skipQuoted(q string, i int, quote byte, esc bool) int {
	for j := i + 1; j < len(q); j++ {
		switch q[j] {
		case '\\':
			if esc {
				j++
			}
		case quote:
			if j+1 < len(q) && q[j+1] == quote {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(q)
}

// dollarQuoteEnd returns the index past a Postgres $tag$...$tag$ string that
// starts at q[i], or 0 when q[i] does not start one (for example $1).
func dollarQuoteEnd(q string, i int) int {
	j := i + 1
	for j < len(q) && q[j] != '$' {
		if !isIdent(q[j]) || (j == i+1 && q[j] >= '0' && q[j] <= '9') {
			return 0
		}
		j++
	}
	if j >= len(q) {
		return 0
	}
	tag := q[i : j+1]
	k := strings.Index(q[j+1:], tag)
	if k < 0 {
		return len(q)
	}
	return j + 1 + k + len(tag)
}

// render joins the parts with the dialect's placeholders. sizes[i] > 0 means
// argument i is an [In] list of that many values; nil means none is.
func (c *compiled) render(d Dialect, sizes []int) string {
	var b strings.Builder
	n := 0
	for i, p := range c.parts {
		b.WriteString(p)
		if i == len(c.parts)-1 {
			break
		}
		k := 1
		if sizes != nil && sizes[i] > 0 {
			k = sizes[i]
		}
		for j := 0; j < k; j++ {
			if j > 0 {
				b.WriteString(", ")
			}
			n++
			b.WriteString(d.Placeholder(n))
		}
	}
	return b.String()
}

// In expands a slice into a comma separated list of placeholders, for
// WHERE id IN (?):
//
//	db.Select(ctx, &users, "SELECT * FROM users WHERE id IN (?)", sql.In(ids))
//
// An empty slice is an error, because IN () is not valid SQL.
func In(slice any) any { return inList{reflect.ValueOf(slice)} }

type inList struct{ v reflect.Value }

// ErrPlaceholders means a query's ? placeholders do not match its arguments.
var ErrPlaceholders = errors.New("sql: placeholders do not match arguments")

// bind returns the query text for the dialect and the arguments with any
// [In] list flattened.
func (db *DB) bind(q string, args []any) (string, []any, error) {
	c := db.queries.get(db.dialect, q)
	var sizes []int
	named := false
	for i, a := range args {
		switch a := a.(type) {
		case inList:
			if a.v.Kind() != reflect.Slice && a.v.Kind() != reflect.Array {
				return "", nil, fmt.Errorf("sql.In: want a slice, got %s", a.v.Kind())
			}
			if a.v.Len() == 0 {
				return "", nil, errors.New("sql.In: empty list")
			}
			if sizes == nil {
				sizes = make([]int, len(args))
			}
			sizes[i] = a.v.Len()
		case stdsql.NamedArg:
			named = true
		}
	}
	if named {
		return q, args, nil // the driver binds named arguments itself
	}
	if c.placeholders() == 0 {
		// No ?: the query has no arguments, or uses the database's own
		// placeholders ($1 on Postgres). Pass it through.
		return q, args, nil
	}
	if c.placeholders() != len(args) {
		return "", nil, fmt.Errorf("%w: %d placeholders, %d arguments", ErrPlaceholders, c.placeholders(), len(args))
	}
	if sizes == nil {
		return c.plain, args, nil
	}
	flat := make([]any, 0, len(args)+8)
	for _, a := range args {
		if l, ok := a.(inList); ok {
			for j := 0; j < l.v.Len(); j++ {
				flat = append(flat, l.v.Index(j).Interface())
			}
			continue
		}
		flat = append(flat, a)
	}
	return c.render(db.dialect, sizes), flat, nil
}

// native renders q, written with ?, in the dialect's placeholders without
// the query cache, for statements whose text varies (multi row INSERTs).
func (db *DB) native(q string, nargs int) string {
	if nargs == 0 || db.dialect.Placeholder(1) == "?" && !strings.Contains(q, "??") {
		return q
	}
	return compile(q, db.dialect.BackslashEscapes()).render(db.dialect, nil)
}
