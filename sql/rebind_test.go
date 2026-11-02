package sql

import (
	"strconv"
	"strings"
	"testing"
)

type pgLike struct{ mysqlLike }

func (pgLike) Placeholder(n int) string { return "$" + strconv.Itoa(n) }
func (pgLike) BackslashEscapes() bool   { return false }

type mysqlLike struct{}

func (mysqlLike) Name() string                  { return "test" }
func (mysqlLike) DriverName() string            { return "none" }
func (mysqlLike) DSN(u string) (string, error)  { return u, nil }
func (mysqlLike) Classify(error) (Kind, string) { return KindOther, "" }
func (mysqlLike) Placeholder(int) string        { return "?" }
func (mysqlLike) BackslashEscapes() bool        { return true }
func (mysqlLike) Quote(s string) string         { return s }
func (mysqlLike) Returning() bool               { return false }

func TestRebindPostgres(t *testing.T) {
	db := &DB{dialect: pgLike{}}
	cases := []struct {
		in, want string
		n        int
	}{
		{"SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = $1 AND b = $2", 2},
		{"SELECT '?' , \"?\", ? -- ?\n, ?", "SELECT '?' , \"?\", $1 -- ?\n, $2", 2},
		{"SELECT /* ? */ ?", "SELECT /* ? */ $1", 1},
		{"SELECT 'it''s ?', ?", "SELECT 'it''s ?', $1", 1},
		{"SELECT E'\\' ?', ?", "SELECT E'\\' ?', $1", 1},
		{"SELECT $$ ? $$, $tag$ ? $tag$, ?", "SELECT $$ ? $$, $tag$ ? $tag$, $1", 1},
		{"SELECT data ?? 'k', data ??| ?", "SELECT data ? 'k', data ?| $1", 1},
		{"SELECT ?::int", "SELECT $1::int", 1},
	}
	for _, c := range cases {
		args := make([]any, c.n)
		got, _, err := db.bind(c.in, args)
		if err != nil || got != c.want {
			t.Errorf("bind(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestRebindMySQL(t *testing.T) {
	db := &DB{dialect: mysqlLike{}}
	got, _, err := db.bind(`SELECT 'a\' ?', ?, `+"`?`"+` # ?
	FROM t`, []any{1})
	if err != nil || !strings.Contains(got, "'a\\' ?', ?, `?` # ?") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestBindIn(t *testing.T) {
	db := &DB{dialect: pgLike{}}
	q, args, err := db.bind("SELECT * FROM t WHERE a = ? AND id IN (?) AND b = ?", []any{"x", In([]int{1, 2, 3}), true})
	if err != nil {
		t.Fatal(err)
	}
	if q != "SELECT * FROM t WHERE a = $1 AND id IN ($2, $3, $4) AND b = $5" || len(args) != 5 || args[3] != 3 || args[4] != true {
		t.Fatalf("q = %q args = %v", q, args)
	}
	if _, _, err := db.bind("SELECT ? IN (?)", []any{1, In([]int{})}); err == nil {
		t.Fatal("empty In: want error")
	}
	if _, _, err := db.bind("SELECT ?", []any{In(3)}); err == nil {
		t.Fatal("In of a non-slice: want error")
	}
}

func TestBindCounts(t *testing.T) {
	db := &DB{dialect: pgLike{}}
	if _, _, err := db.bind("SELECT ?, ?", []any{1}); err == nil {
		t.Fatal("want ErrPlaceholders")
	}
	q, _, err := db.bind("SELECT $1, $2", []any{1, 2}) // native placeholders pass through
	if err != nil || q != "SELECT $1, $2" {
		t.Fatalf("native: %q %v", q, err)
	}
}

func TestBindAllocs(t *testing.T) {
	db := &DB{dialect: pgLike{}}
	q := "SELECT * FROM users WHERE id = ? AND tenant = ?"
	args := []any{1, 2}
	_, _, _ = db.bind(q, args)
	if n := testing.AllocsPerRun(100, func() { _, _, _ = db.bind(q, args) }); n > 0 {
		t.Fatalf("bind allocates %v times after the first call", n)
	}
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{
		"ID": "id", "UserID": "user_id", "CreatedAt": "created_at", "HTTPServer": "http_server", "Name": "name",
	} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzCompile checks that rewriting never panics, keeps every byte outside
// placeholders, and that a query with no special characters is unchanged.
func FuzzCompile(f *testing.F) {
	for _, s := range []string{"SELECT ?", "'?", "$$?", "$a$ ? $a$ ?", "-- ?\n?", "/* ?", "E'\\'?'", "??", "`?`"} {
		f.Add(s, true)
		f.Add(s, false)
	}
	f.Fuzz(func(t *testing.T, q string, backslash bool) {
		c := compile(q, backslash)
		joined := strings.Join(c.parts, "?")
		if strings.ReplaceAll(q, "??", "?") != joined && !strings.Contains(q, "??") {
			t.Fatalf("compile(%q) lost text: %q", q, joined)
		}
		if !strings.ContainsAny(q, "?'\"`-/$#") && len(c.parts) != 1 {
			t.Fatalf("compile(%q) split a plain query", q)
		}
	})
}
