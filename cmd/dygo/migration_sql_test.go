package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSplitSQL(t *testing.T) {
	src := "-- a comment; not a statement\nCREATE TABLE a (s TEXT DEFAULT 'x;y');\n/* b; */ INSERT INTO a VALUES ('it''s');\n" +
		"CREATE FUNCTION f() RETURNS int AS $body$ SELECT 1; $body$ LANGUAGE sql;\nSELECT `a;b` FROM t"
	got := splitSQL(src)
	want := []string{
		"CREATE TABLE a (s TEXT DEFAULT 'x;y')",
		"INSERT INTO a VALUES ('it''s')",
		"CREATE FUNCTION f() RETURNS int AS $body$ SELECT 1; $body$ LANGUAGE sql",
		"SELECT `a;b` FROM t",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func columnNames(t *tableState) []string {
	var out []string
	for _, c := range t.Columns {
		out = append(out, c.Name)
	}
	return out
}

func TestReplayMigrations(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"0001_init.up.sql": `CREATE TABLE IF NOT EXISTS public.users (
			id BIGSERIAL PRIMARY KEY,
			"Email" TEXT NOT NULL UNIQUE,
			price NUMERIC(10, 2) NOT NULL,
			CONSTRAINT users_email UNIQUE ("Email"),
			PRIMARY KEY (id)
		);
		CREATE TABLE old (id INT);
		CREATE INDEX users_price ON users (price);`,
		"0001_init.down.sql": "DROP TABLE users;",
		"0002_more.up.sql": `ALTER TABLE users ADD COLUMN name TEXT, ADD CONSTRAINT c CHECK (price > 0), DROP COLUMN IF EXISTS price;
		ALTER TABLE users RENAME COLUMN name TO full_name;
		ALTER TABLE ` + "`users`" + ` ADD age INT NOT NULL DEFAULT 0, ADD INDEX users_age (age);
		DROP TABLE IF EXISTS old CASCADE;
		CREATE TABLE t1 (id INT);
		RENAME TABLE t1 TO t2;
		ALTER TABLE t2 CHANGE COLUMN id ident BIGINT, RENAME TO t3;
		ALTER TABLE missing ADD COLUMN x INT;`,
	}
	for name, src := range files {
		write(t, filepath.Join(dir, name), src)
	}
	tables, err := replayMigrations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tables["old"]; ok {
		t.Error("old was dropped")
	}
	if got := columnNames(tables["users"]); !slices.Equal(got, []string{"id", "Email", "full_name", "age"}) {
		t.Errorf("users: %q", got)
	}
	if got := tables["users"].Columns[3].Def; got != "INT NOT NULL DEFAULT 0" {
		t.Errorf("age def: %q", got)
	}
	if t3 := tables["t3"]; t3 == nil || !slices.Equal(columnNames(t3), []string{"ident"}) {
		t.Errorf("t3: %+v (tables %v)", t3, tables)
	}
}

func TestEntityColumns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "e.go")
	write(t, file, "package e\n\nimport (\n\t\"database/sql\"\n\t\"time\"\n)\n\ntype Item struct {\n"+
		"\tKey string `db:\"key,pk\"`\n"+
		"\tUserID int64\n"+
		"\tNote string `json:\"note,omitempty\"`\n"+
		"\tSeen *time.Time\n"+
		"\tScore sql.NullFloat64\n"+
		"\tCreatedAt time.Time `db:\"created_at,created\"`\n"+
		"\tSkip string `db:\"-\"`\n"+
		"\thidden int\n"+
		"\tTags []string\n}\n")
	for _, mysql := range []bool{false, true} {
		cols, err := entityColumns(file, "Item", mysql)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range cols {
			got = append(got, c.Name+" "+c.Def)
		}
		want := []string{
			"key TEXT PRIMARY KEY", "user_id BIGINT NOT NULL", "note TEXT NOT NULL DEFAULT ''", "seen TIMESTAMPTZ",
			"score DOUBLE PRECISION", "created_at TIMESTAMPTZ NOT NULL DEFAULT now()", "tags TEXT NOT NULL",
		}
		if mysql {
			want = []string{
				"key VARCHAR(255) PRIMARY KEY", "user_id BIGINT NOT NULL", "note VARCHAR(255) NOT NULL DEFAULT ''", "seen DATETIME(6)",
				"score DOUBLE", "created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)", "tags TEXT NOT NULL",
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("mysql=%v:\n got %q\nwant %q", mysql, got, want)
		}
		if cols[len(cols)-1].Note == "" {
			t.Error("[]string has no note")
		}
	}
}

func TestEntityMigration(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "order.go")
	write(t, file, "package orders\n\ntype OrderItem struct {\n\tID int `json:\"id\"`\n\tOrder string\n\tQty int\n}\n")
	e := entityPkg{Dir: dir, File: file, Entity: "OrderItem"}
	mig := filepath.Join(dir, "migrations")
	got, err := entityMigration(e, "order_items", mig, true, migrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE TABLE order_items (\n    id      BIGINT AUTO_INCREMENT PRIMARY KEY,\n    `order` VARCHAR(255) NOT NULL,\n    qty     BIGINT NOT NULL\n);\n"
	if got.Up != want || got.Down != "DROP TABLE order_items;\n" {
		t.Fatalf("create:\n%s\n%s", got.Up, got.Down)
	}
	write(t, filepath.Join(mig, "0001_x.up.sql"), "CREATE TABLE order_items (id BIGINT PRIMARY KEY, `order` TEXT NOT NULL, gone INT NOT NULL);")
	got, err = entityMigration(e, "order_items", mig, true, migrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Up, "ALTER TABLE order_items ADD COLUMN qty BIGINT NOT NULL DEFAULT 0;") ||
		!strings.Contains(got.Up, "ALTER TABLE order_items DROP COLUMN gone;") ||
		got.Down != "ALTER TABLE order_items ADD COLUMN gone INT NOT NULL;\nALTER TABLE order_items DROP COLUMN qty;\n" ||
		got.Summary != "ALTER TABLE order_items: add qty; drop gone" {
		t.Fatalf("alter:\n%s\n%s\n%s", got.Up, got.Down, got.Summary)
	}
}

func TestEntityForMigration(t *testing.T) {
	entities := []entityPkg{{Entity: "Product"}, {Entity: "OrderItem"}, {Entity: "Person"}}
	tables := map[string]string{"Product": "products", "OrderItem": "order_items", "Person": "people"}
	for name, want := range map[string]string{
		"create_products":        "Product",
		"add_price_to_products":  "Product",
		"update_order_items":     "OrderItem",
		"create_product":         "Product",
		"people":                 "Person",
		"remove_qty_from_orders": "",
		"create_users":           "",
	} {
		e, ok := entityForMigration(entities, tables, splitWords(name))
		if e.Entity != want || ok != (want != "") {
			t.Errorf("%s: got %q, want %q", name, e.Entity, want)
		}
	}
}

func TestTableName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "m.go"), "package m\n\nvar p = sql.ForFeature[products.Product](\"catalog_items\")\nvar r, _ = sql.NewRepository[Box](db, \"crates\")\n")
	repo := repoTables(dir)
	for entity, want := range map[string]string{"Product": "catalog_items", "Box": "crates", "OrderItem": "order_items", "Category": "categories"} {
		if got := tableName(repo, entity); got != want {
			t.Errorf("%s: %q, want %q", entity, got, want)
		}
	}
	if snakeCase("UserID") != "user_id" || snakeCase("HTTPServer") != "http_server" {
		t.Error("snakeCase")
	}
	if quoteIdent("user", false) != `"user"` || quoteIdent("Name", true) != "`Name`" || quoteIdent("title", true) != "title" {
		t.Error("quoteIdent")
	}
}

func TestAlterDef(t *testing.T) {
	for _, c := range []struct{ def, action, want string }{
		{"INT NOT NULL DEFAULT 0", "TYPE BIGINT USING price::bigint", "BIGINT NOT NULL DEFAULT 0"},
		{"NUMERIC(10, 2) NOT NULL", "SET DATA TYPE DOUBLE PRECISION", "DOUBLE PRECISION NOT NULL"},
		{"TEXT", "SET NOT NULL", "TEXT NOT NULL"},
		{"TEXT NULL", "set not null", "TEXT NOT NULL"},
		{"TEXT NOT NULL DEFAULT ''", "DROP NOT NULL", "TEXT DEFAULT ''"},
		{"TEXT NOT NULL DEFAULT ''", "DROP DEFAULT", "TEXT NOT NULL"},
		{"BIGINT NOT NULL DEFAULT 0", "SET DEFAULT 7", "BIGINT NOT NULL DEFAULT 7"},
	} {
		if got := alterDef(c.def, c.action); got != c.want {
			t.Errorf("alterDef(%q, %q) = %q, want %q", c.def, c.action, got, c.want)
		}
	}
	for typ, kind := range map[string]string{
		"VARCHAR(255)": "text", "CHARACTER VARYING(20)": "text", "INT4": "integer", "BIGINT UNSIGNED": "integer",
		"TINYINT(1)": "bool", "DOUBLE PRECISION": "float", "NUMERIC(10, 2)": "float", "TIMESTAMP WITH TIME ZONE": "time",
		"DATETIME(6)": "time", "JSONB": "json", "POINT": "",
	} {
		if got := typeKind(typ); got != kind {
			t.Errorf("typeKind(%q) = %q, want %q", typ, got, kind)
		}
	}
	if got := defType("VARCHAR(20) CHARACTER SET utf8mb4 NOT NULL"); got != "VARCHAR(20)" {
		t.Errorf("defType: %q", got)
	}
}

func TestRenameAndTypeChange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "p.go")
	write(t, file, "package products\n\ntype Product struct {\n\tID int `json:\"id\"`\n\tDescription string\n\tPrice float64\n\tStock *int\n\tTitle string\n\tCount int\n}\n")
	e := entityPkg{Dir: dir, File: file, Entity: "Product"}
	mig := filepath.Join(dir, "migrations")
	write(t, filepath.Join(mig, "0001_x.up.sql"), "CREATE TABLE products (id BIGSERIAL PRIMARY KEY, note TEXT NOT NULL DEFAULT '', price INT NOT NULL DEFAULT 0, stock BIGINT NOT NULL, title VARCHAR(80), size INT);")

	var asked []string
	ask := func(q string) bool { asked = append(asked, q); return strings.Contains(q, "note to description") }
	got, err := entityMigration(e, "products", mig, false, migrationOptions{Ask: ask})
	if err != nil {
		t.Fatal(err)
	}
	// size is an integer and count a new integer: asked, answered no.
	if len(asked) != 2 || !strings.Contains(asked[1], "products.size to count (BIGINT)") {
		t.Errorf("asked %q", asked)
	}
	for _, want := range []string{
		"ALTER TABLE products RENAME COLUMN note TO description;",
		"ALTER TABLE products ALTER COLUMN price DROP DEFAULT, ALTER COLUMN price TYPE DOUBLE PRECISION USING price::double precision;",
		"ALTER TABLE products ALTER COLUMN stock DROP NOT NULL;",
		"UPDATE products SET title = '' WHERE title IS NULL;\nALTER TABLE products ALTER COLUMN title SET NOT NULL;",
		"-- If count replaces it, run dygo g migration again with -rename size:count to keep the data.",
		"ALTER TABLE products ADD COLUMN count BIGINT NOT NULL DEFAULT 0;",
	} {
		if !strings.Contains(got.Up, want) {
			t.Errorf("up misses %q:\n%s", want, got.Up)
		}
	}
	for _, want := range []string{
		"ALTER TABLE products RENAME COLUMN description TO note;",
		"ALTER TABLE products ALTER COLUMN price TYPE INT USING price::int, ALTER COLUMN price SET DEFAULT 0;",
		"UPDATE products SET stock = 0 WHERE stock IS NULL;\nALTER TABLE products ALTER COLUMN stock SET NOT NULL;",
		"ALTER TABLE products ALTER COLUMN title DROP NOT NULL;",
	} {
		if !strings.Contains(got.Down, want) {
			t.Errorf("down misses %q:\n%s", want, got.Down)
		}
	}
	// INT to DOUBLE PRECISION: a warning, in the SQL and for the output.
	if !strings.Contains(got.Up, "-- price changes from INT to DOUBLE PRECISION.\n-- DATA LOSS: large integers lose precision in DOUBLE PRECISION; the down migration cannot bring them back.") ||
		!strings.Contains(got.Down, "-- price changes from DOUBLE PRECISION to INT.\n-- DATA LOSS: decimals are rounded (2.7 becomes 3), for the values written since the up migration.") {
		t.Errorf("type change comments:\n%s\n%s", got.Up, got.Down)
	}
	if len(got.Warnings) != 1 || !strings.HasPrefix(got.Warnings[0], "products.price INT to DOUBLE PRECISION loses data: large integers lose precision") {
		t.Errorf("warnings %q", got.Warnings)
	}
	if strings.Index(got.Down, "DROP COLUMN count") > strings.Index(got.Down, "RENAME COLUMN description") {
		t.Errorf("down is not in reverse order:\n%s", got.Down)
	}

	// MySQL, with -rename instead of the question.
	got, err = entityMigration(e, "products", mig, true, migrationOptions{Renames: map[string]string{"note": "description", "size": "count"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ALTER TABLE products RENAME COLUMN size TO count;",
		"ALTER TABLE products MODIFY COLUMN price DOUBLE NOT NULL;",
		"UPDATE products SET title = '' WHERE title IS NULL;\nALTER TABLE products MODIFY COLUMN title VARCHAR(255) NOT NULL;",
	} {
		if !strings.Contains(got.Up, want) {
			t.Errorf("mysql up misses %q:\n%s", want, got.Up)
		}
	}
	if !strings.Contains(got.Down, "ALTER TABLE products MODIFY COLUMN price INT NOT NULL DEFAULT 0;") {
		t.Errorf("mysql down:\n%s", got.Down)
	}

	for renames, want := range map[string]string{
		"missing:count":    "table products has no column missing",
		"title:count":      "title is still a field of Product",
		"note:whatever":    "whatever is not a new field of Product",
		"note:title":       "title is not a new field of Product",
		"size:description": "",
	} {
		r, err := parseRenames(renames)
		if err != nil {
			t.Fatal(err)
		}
		_, err = entityMigration(e, "products", mig, false, migrationOptions{Renames: r})
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("-rename %s: %v, want %q", renames, err, want)
		}
	}
	if _, err := parseRenames("note"); err == nil {
		t.Error("parseRenames without a colon")
	}

	// Replaying the generated SQL gives a table that matches the entity.
	pg, _ := entityMigration(e, "products", mig, false, migrationOptions{Renames: map[string]string{"note": "description", "size": "count"}})
	write(t, filepath.Join(mig, "0002_y.up.sql"), pg.Up)
	again, err := entityMigration(e, "products", mig, false, migrationOptions{Ask: func(string) bool { t.Error("asked again"); return false }})
	if err != nil || again.Up != "" {
		t.Fatalf("after replay: %v\n%s", err, again.Up)
	}
}

func TestSafeMigration(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "p.go")
	write(t, file, "package products\n\ntype Product struct {\n\tID int `json:\"id\"`\n\tSummary string `json:\"summary,omitempty\"`\n\tStock *int\n\tCount int\n}\n")
	e := entityPkg{Dir: dir, File: file, Entity: "Product"}
	mig := filepath.Join(dir, "migrations")
	write(t, filepath.Join(mig, "0001_x.up.sql"), "CREATE TABLE products (id BIGSERIAL PRIMARY KEY, note TEXT NOT NULL DEFAULT 'x', stock INT NOT NULL, legacy TEXT);")
	o := migrationOptions{Safe: true, Renames: map[string]string{"note": "summary"}}

	got, err := entityMigration(e, "products", mig, true, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ALTER TABLE products MODIFY COLUMN stock BIGINT;", // NULL change, in the expand
		"ALTER TABLE products ADD COLUMN count BIGINT NOT NULL DEFAULT 0;",
		"ALTER TABLE products ADD COLUMN summary VARCHAR(255);",
		"UPDATE products SET summary = COALESCE(note, '');",
		"ALTER TABLE products ALTER COLUMN note DROP DEFAULT;",
		"CREATE TRIGGER dygo_sync_products_note_ins BEFORE INSERT ON products FOR EACH ROW SET NEW.summary = COALESCE(NEW.summary, NEW.note), NEW.note = COALESCE(NEW.note, NEW.summary);",
		"CREATE TRIGGER dygo_sync_products_note_upd BEFORE UPDATE ON products FOR EACH ROW SET NEW.summary = IF(NEW.note <=> OLD.note, NEW.summary, NEW.note), NEW.note = NEW.summary;",
		"ALTER TABLE products MODIFY COLUMN summary VARCHAR(255) NOT NULL;",
	} {
		if !strings.Contains(got.Up, want) {
			t.Errorf("expand up misses %q:\n%s", want, got.Up)
		}
	}
	if strings.Contains(got.Up, "DROP COLUMN") {
		t.Errorf("expand drops a column:\n%s", got.Up)
	}
	for _, want := range []string{
		"DROP TRIGGER dygo_sync_products_note_upd;\nDROP TRIGGER dygo_sync_products_note_ins;\nALTER TABLE products DROP COLUMN note;\nALTER TABLE products ALTER COLUMN summary SET DEFAULT '';",
		"ALTER TABLE products DROP COLUMN legacy;",
	} {
		if !strings.Contains(got.ContractUp, want) {
			t.Errorf("contract up misses %q:\n%s", want, got.ContractUp)
		}
	}
	for _, want := range []string{"ALTER TABLE products ADD COLUMN note TEXT;", "UPDATE products SET note = COALESCE(summary, '');", "MODIFY COLUMN note TEXT NOT NULL;", "CREATE TRIGGER dygo_sync_products_note_ins"} {
		if !strings.Contains(got.ContractDown, want) {
			t.Errorf("contract down misses %q:\n%s", want, got.ContractDown)
		}
	}
	// The expand down undoes the expand in reverse: the sync goes before the
	// added column and the NULL change.
	if i, j := strings.Index(got.Down, "DROP TRIGGER"), strings.Index(got.Down, "DROP COLUMN count"); i < 0 || j < i {
		t.Errorf("expand down order:\n%s", got.Down)
	}

	// Postgres casts between the two types in the trigger.
	write(t, file, "package products\n\ntype Product struct {\n\tID int `json:\"id\"`\n\tNote string\n\tStock *int\n\tCount int\n\tLegacy *string\n\tCents int64\n}\n")
	got, err = entityMigration(e, "products", mig, false, migrationOptions{Safe: true})
	if err != nil || got.ContractUp != "" {
		t.Fatalf("no rename: %v\ncontract:\n%s", err, got.ContractUp)
	}

	// A type change under the same name is refused with a hint.
	write(t, file, "package products\n\ntype Product struct {\n\tID int `json:\"id\"`\n\tNote int\n\tStock *int\n\tLegacy *string\n}\n")
	if _, err := entityMigration(e, "products", mig, false, migrationOptions{Safe: true}); err == nil || !strings.Contains(err.Error(), `db:"note_v2"`) {
		t.Fatalf("same-name type change: %v", err)
	}
	write(t, file, "package products\n\ntype Product struct {\n\tID int `json:\"id\"`\n\tNoteV2 int\n\tStock *int\n\tLegacy *string\n}\n")
	got, err = entityMigration(e, "products", mig, false, migrationOptions{Safe: true, Renames: map[string]string{"note": "note_v2"}})
	if err != nil || !strings.Contains(got.Up, "NEW.note_v2 := NEW.note::bigint;") || !strings.Contains(got.Up, "NEW.note := NEW.note_v2::text;") {
		t.Fatalf("rename with a new type: %v\n%s", err, got.Up)
	}
}

func TestTypeChangeRisk(t *testing.T) {
	for _, tc := range []struct {
		from, to   string
		mysql      bool
		lost, fail string
	}{
		{"NUMERIC(10, 2)", "BIGINT", false, "decimals are rounded (2.7 becomes 3)", "numbers too big for BIGINT"},
		{"INT", "BIGINT", false, "", ""},
		{"INT", "NUMERIC", false, "", ""},
		{"BIGINT", "NUMERIC(5, 2)", false, "", "numbers too big for NUMERIC(5, 2)"},
		{"BIGINT", "REAL", false, "large integers lose precision in REAL", ""},
		{"INT", "BOOLEAN", false, "every number other than 0 becomes true (2 and -1 read back as 1)", ""},
		{"INT", "TINYINT(1)", true, "", "numbers outside -128 to 127"},
		{"DOUBLE PRECISION", "BOOLEAN", false, "", "every row: Postgres has no cast from DOUBLE PRECISION to BOOLEAN"},
		{"BOOLEAN", "INT", false, "", ""},
		{"TEXT", "BIGINT", false, "", "text that is not a valid BIGINT value"},
		{"TEXT", "TIMESTAMPTZ", true, "", "text that is not a valid TIMESTAMPTZ value"},
		{"BIGINT", "TEXT", false, "", ""},
		{"BIGINT", "VARCHAR(5)", false, "values longer than 5 characters are cut", ""},
		{"BIGINT", "VARCHAR(5)", true, "", "values longer than 5 characters"},
		{"BYTEA", "TEXT", false, "bytes become hex text such as \\x6869, not the decoded text", ""},
		{"BLOB", "TEXT", true, "", "bytes that are not valid text in the column's character set"},
		{"TIMESTAMPTZ", "BIGINT", false, "", "values that do not convert from TIMESTAMPTZ to BIGINT"},
	} {
		got := typeChangeRisk(tc.from, tc.to, tc.mysql)
		if got.Lost != tc.lost || got.Fail != tc.fail {
			t.Errorf("%s to %s (mysql %v) = %+v, want lost %q fail %q", tc.from, tc.to, tc.mysql, got, tc.lost, tc.fail)
		}
	}

	safe := typeChangeRisk("TEXT", "INT", true).comment("qty", "TEXT", "INT", true, stepSafe)
	if want := "-- MAY FAIL on text that is not a valid INT value. While the sync runs, writes of such values fail too.\n"; !strings.HasSuffix(safe, want) {
		t.Errorf("safe comment:\n%s", safe)
	}
	if got := typeChangeRisk("INT", "BIGINT", false).comment("qty", "INT", "BIGINT", false, stepUp); got != "-- qty changes from INT to BIGINT; every value converts.\n" {
		t.Errorf("safe change comment %q", got)
	}
	mysql := typeChangeRisk("TEXT", "INT", true).warnings("t.qty", "TEXT", "INT", stepUp)
	if len(mysql) != 1 || mysql[0] != "t.qty TEXT to INT fails on text that is not a valid INT value: check the data first." {
		t.Errorf("warnings %q", mysql)
	}

	// -safe: a rename that changes the kind warns and comments the copy.
	s := newSync("products", sqlColumn{Name: "qty", Def: "TEXT"}, sqlColumn{Name: "quantity", Def: "BIGINT NOT NULL"}, false)
	if up := s.expandUp(); !strings.Contains(up, "-- qty into quantity changes from TEXT to BIGINT.\n-- MAY FAIL on text that is not a valid BIGINT value.") {
		t.Errorf("expand:\n%s", up)
	}
}

// TestFillEveryEntity: every entity whose
// table differs is filled, whatever the migration is called.
func TestFillEveryEntity(t *testing.T) {
	dir := t.TempDir()
	for _, e := range []string{"User", "Product"} {
		write(t, filepath.Join(dir, strings.ToLower(e)+".go"), "package app\n\ntype "+e+" struct {\n\tID int `json:\"id\"`\n\tName string\n}\n\ntype Create"+e+" struct{ Name string }\n\ntype Update"+e+" struct{ Name *string }\n")
	}
	mig := filepath.Join(dir, "migrations")
	write(t, filepath.Join(mig, "0001_x.up.sql"), "CREATE TABLE users (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL);")
	got, err := fillMigration(project{Dir: dir}, mig, []string{"users", "products", "migration"}, migrationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Up, "CREATE TABLE products") || strings.Contains(got.Up, "CREATE TABLE users") || got.Down != "DROP TABLE products;\n" {
		t.Fatalf("up:\n%s\ndown:\n%s", got.Up, got.Down)
	}

	write(t, filepath.Join(mig, "0002_y.up.sql"), got.Up)
	if got, err = fillMigration(project{Dir: dir}, mig, []string{"anything"}, migrationOptions{}); err != nil || got.Up != "" {
		t.Fatalf("after replay: %v\n%s", err, got.Up)
	}
}

func TestAddIDTag(t *testing.T) {
	src := []byte("package p\n\ntype Product struct {\n\tID    int     `json:\"id\"`\n\tTitle string  `json:\"title\"`\n}\n")
	out, ok := addIDTag(src, "Product")
	if !ok || !strings.Contains(string(out), "ID    int    `json:\"id\" db:\"id,pk,auto\"`") {
		t.Fatalf("addIDTag = %v\n%s", ok, out)
	}
	if _, ok := addIDTag(out, "Product"); ok {
		t.Fatal("added the tag twice")
	}
}

func TestRepoEntities(t *testing.T) {
	dir := t.TempDir()
	sec := filepath.Join(dir, "security")
	if err := os.MkdirAll(sec, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package security

type User struct{ ID int }

type Token struct{ Hash string ` + "`db:\"hash,pk\"`" + ` }

func repos(d *sql.DB) {
	sql.NewRepository[User](d, "users")
	sql.NewRepository[Token](d, "refresh_tokens")
	sql.NewRepository[other.Thing](d, "things") // declared elsewhere: not this file's entity
}
`
	if err := os.WriteFile(filepath.Join(sec, "users.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	known := []entityPkg{{Dir: sec, File: filepath.Join(sec, "users.go"), Entity: "User"}}
	got := repoEntities(dir, known)
	if len(got) != 1 || got[0].Entity != "Token" || got[0].Dir != sec {
		t.Fatalf("repoEntities = %+v, want only Token", got)
	}
	if got := repoEntities(dir, nil); len(got) != 2 {
		t.Fatalf("repoEntities without known = %+v, want User and Token", got)
	}
}
