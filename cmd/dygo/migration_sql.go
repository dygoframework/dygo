package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// This file fills dygo g migration from an entity. It compares the entity
// with the table that the earlier migration files build (it replays their
// CREATE, ALTER and DROP TABLE statements), not with a live database, so
// the CLI needs no driver. Renames are confirmed by the user (a question in
// a terminal, or -rename old:new); a type is changed only when its kind
// changes (text, integer, float, ...), so TEXT and VARCHAR(255) for a Go
// string are both left alone. The result is a starting point to review.

// sqlColumn is a column as an entity defines it or the migrations left it.
type sqlColumn struct {
	Name string
	Def  string // the rest of the definition: TEXT NOT NULL
	Note string // a comment for the reader, such as a type to check
	zero string // the default an added NOT NULL column gets for existing rows
	kind string // the type's kind (see typeKind); "" when unknown
}

// tableState is a table built by the migration files.
type tableState struct {
	Columns []sqlColumn
}

func (t *tableState) index(name string) int {
	return slices.IndexFunc(t.Columns, func(c sqlColumn) bool { return strings.EqualFold(c.Name, name) })
}

// migrationSQL is what dygo g migration writes for an entity.
type migrationSQL struct {
	Up, Down string
	// ContractUp and ContractDown are the second migration of -safe, which
	// waits for dygo db migrate --contract; "" without one.
	ContractUp, ContractDown string
	Summary                  string   // one line for the output; "" when nothing changed
	Message                  string   // what fillMigration tells the user
	Warnings                 []string // type changes that can lose data or fail
}

// fillMigration returns the SQL that brings every entity's table up to date
// with the entity, with a message for the user. Up is "" when every table
// matches. With -rename or -safe it works on the one entity the migration
// name is about (see entityForMigration).
func fillMigration(proj project, dir string, words []string, o migrationOptions) (migrationSQL, error) {
	entities, err := listEntities(proj.Dir)
	if err != nil {
		return migrationSQL{}, err
	}
	entities = append(entities, repoEntities(proj.Dir, entities)...)
	if len(entities) == 0 {
		return migrationSQL{}, nil
	}
	repo := repoTables(proj.Dir)
	tables := map[string]string{}
	for _, e := range entities {
		tables[e.Entity] = tableName(repo, e.Entity)
	}
	// Every entity is compared with the migrations; the name is only a label.
	// -rename and -safe work on one table, which the name picks.
	picked := slices.Clone(entities)
	slices.SortFunc(picked, func(a, b entityPkg) int { return strings.Compare(tables[a.Entity], tables[b.Entity]) })
	if len(o.Renames) > 0 || o.Safe {
		e, ok := entityForMigration(entities, tables, words)
		if !ok {
			return migrationSQL{}, fmt.Errorf("-rename and -safe need a migration named after an entity's table, such as update_%s", tables[entities[0].Entity])
		}
		picked = []entityPkg{e}
	}
	mysql, known := projectDialect(proj)
	var out migrationSQL
	var filled, ups, downs, sums []string
	for _, e := range picked {
		m, err := entityMigration(e, tables[e.Entity], dir, mysql, o)
		if err != nil {
			return migrationSQL{}, err
		}
		src := rel(proj.Dir, e.File)
		if m.Up == "" {
			continue
		}
		if len(picked) == 1 {
			out = m
		}
		ups, downs, sums = append(ups, m.Up), append(downs, m.Down), append(sums, m.Summary)
		out.Warnings = append(out.Warnings, m.Warnings...)
		filled = append(filled, fmt.Sprintf("%s (%s)", e.Entity, src))
	}
	if len(picked) > 1 && len(ups) > 0 {
		slices.Reverse(downs)
		out.Up, out.Down, out.Summary = strings.Join(ups, "\n"), strings.Join(downs, "\n"), strings.Join(sums, "; ")
	}
	if out.Up == "" {
		out.Message = "Every table already matches its entity in the migrations, so the files are left for your own SQL."
		return out, nil
	}
	dialect := "Postgres"
	if mysql {
		dialect = "MySQL"
	}
	out.Message = fmt.Sprintf("Filled from %s for %s: %s.", strings.Join(filled, ", "), dialect, out.Summary)
	if !known {
		out.Message += "\nNo DATABASE_URL or driver import found, so the SQL is for Postgres; run dygo db init to pick the database."
	}
	out.Message += "\nIt compares the entities with the earlier migration files, not the database."
	return out, nil
}

// parseRenames parses -rename "note:description, qty:quantity". Field
// names work too: PublishedAt means published_at.
func parseRenames(spec string) (map[string]string, error) {
	out := map[string]string{}
	for _, item := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ' ' }) {
		old, nw, ok := strings.Cut(item, ":")
		if !ok || old == "" || nw == "" {
			return nil, fmt.Errorf("-rename %q: want old:new, such as note:description", item)
		}
		out[snakeCase(old)] = snakeCase(nw)
	}
	return out, nil
}

// rename is a column the user said was renamed: from is the column in the
// table, to the entity's column.
type rename struct{ from, to sqlColumn }

// migrationOptions say how dygo g migration treats renames.
type migrationOptions struct {
	Renames map[string]string   // old column: new column, from -rename
	Ask     func(q string) bool // asks a yes/no question; nil outside a terminal
	Safe    bool                // -safe: expand now, contract after the deploy
}

// entityMigration compares the entity with the table the migrations in dir
// build and returns the SQL that brings the table up to date.
func entityMigration(e entityPkg, table, dir string, mysql bool, o migrationOptions) (migrationSQL, error) {
	cols, err := entityColumns(e.File, e.Entity, mysql)
	if err != nil {
		return migrationSQL{}, err
	}
	tables, err := replayMigrations(dir)
	if err != nil {
		return migrationSQL{}, err
	}
	q := func(name string) string { return quoteIdent(name, mysql) }
	cur, ok := tables[strings.ToLower(table)]
	if !ok {
		for old := range o.Renames {
			return migrationSQL{}, fmt.Errorf("-rename %s: no earlier migration creates table %s", old, table)
		}
		var up strings.Builder
		fmt.Fprintf(&up, "CREATE TABLE %s (\n", q(table))
		width := 0
		for _, c := range cols {
			width = max(width, len(q(c.Name)))
		}
		for i, c := range cols {
			line := fmt.Sprintf("    %-*s %s", width, q(c.Name), c.Def)
			if i < len(cols)-1 {
				line += ","
			}
			if c.Note != "" {
				line += " -- " + c.Note
			}
			up.WriteString(line + "\n")
		}
		up.WriteString(");\n")
		return migrationSQL{
			Up:      up.String(),
			Down:    fmt.Sprintf("DROP TABLE %s;\n", q(table)),
			Summary: "CREATE TABLE " + table,
		}, nil
	}

	// Columns only the entity has, and columns only the table has.
	var added []sqlColumn
	for _, c := range cols {
		if cur.index(c.Name) < 0 {
			added = append(added, c)
		}
	}
	var gone []sqlColumn
	for _, c := range cur.Columns {
		if !slices.ContainsFunc(cols, func(e sqlColumn) bool { return strings.EqualFold(e.Name, c.Name) }) {
			c.kind = typeKind(defType(c.Def))
			gone = append(gone, c)
		}
	}
	find := func(list []sqlColumn, name string) int {
		return slices.IndexFunc(list, func(c sqlColumn) bool { return strings.EqualFold(c.Name, name) })
	}

	// Renames: the ones given with -rename, then a question for each gone
	// column that has an added column of the same kind.
	var renames []rename
	olds := make([]string, 0, len(o.Renames))
	for old := range o.Renames {
		olds = append(olds, old)
	}
	slices.Sort(olds)
	for _, old := range olds {
		nw := o.Renames[old]
		i, j := find(gone, old), find(added, nw)
		switch {
		case i < 0 && cur.index(old) >= 0:
			return migrationSQL{}, fmt.Errorf("-rename %s:%s: %s is still a field of %s", old, nw, old, e.Entity)
		case i < 0:
			return migrationSQL{}, fmt.Errorf("-rename %s:%s: table %s has no column %s", old, nw, table, old)
		case j < 0:
			return migrationSQL{}, fmt.Errorf("-rename %s:%s: %s is not a new field of %s", old, nw, nw, e.Entity)
		}
		renames = append(renames, rename{gone[i], added[j]})
		gone, added = slices.Delete(gone, i, i+1), slices.Delete(added, j, j+1)
	}
	if o.Ask != nil {
		for i := 0; i < len(gone); i++ {
			for j, a := range added {
				if a.kind == "" || a.kind != gone[i].kind {
					continue
				}
				if o.Ask(fmt.Sprintf("Did you rename %s.%s to %s (%s)? [y/N] ", table, gone[i].Name, a.Name, defType(a.Def))) {
					renames = append(renames, rename{gone[i], a})
					gone, added = slices.Delete(gone, i, i+1), slices.Delete(added, j, j+1)
					i--
					break
				}
			}
		}
	}

	if o.Safe {
		return safeMigration(e.Entity, table, cols, cur, renames, added, gone, mysql)
	}
	var up, down, parts, warnings []string
	for _, r := range renames {
		up = append(up, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;", q(table), q(r.from.Name), q(r.to.Name)))
		parts = append(parts, "rename "+r.from.Name+" to "+r.to.Name)
		ch := changeColumn(table, r.to.Name, r.from.Def, r.to, mysql)
		up = append(up, ch.up...)
		down = append(down, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;", q(table), q(r.to.Name), q(r.from.Name)))
		down = append(down, ch.down...)
		parts = append(parts, ch.parts...)
		warnings = append(warnings, ch.warnings...)
	}
	for _, c := range cols {
		i := cur.index(c.Name)
		if i < 0 {
			continue
		}
		ch := changeColumn(table, c.Name, cur.Columns[i].Def, c, mysql)
		up = append(up, ch.up...)
		down = append(down, ch.down...)
		parts = append(parts, ch.parts...)
		warnings = append(warnings, ch.warnings...)
	}
	var addedNames, goneNames []string
	for _, c := range added {
		def := c.Def
		if strings.HasSuffix(def, " NOT NULL") && c.zero != "" {
			def += " DEFAULT " + c.zero // existing rows need a value
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;", q(table), q(c.Name), def)
		if c.Note != "" {
			stmt += " -- " + c.Note
		}
		up = append(up, stmt)
		down = append(down, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", q(table), q(c.Name)))
		addedNames = append(addedNames, c.Name)
	}
	for _, c := range gone {
		hint := ""
		for _, a := range added {
			if a.kind != "" && a.kind == c.kind {
				hint = fmt.Sprintf("\n-- If %s replaces it, run dygo g migration again with -rename %s:%s to keep the data.", a.Name, c.Name, a.Name)
				break
			}
		}
		up = append(up, fmt.Sprintf("-- %s is not a field of %s any more. Dropping it deletes its data:\n-- remove this statement to keep the column.%s\nALTER TABLE %s DROP COLUMN %s;",
			c.Name, e.Entity, hint, q(table), q(c.Name)))
		down = append(down, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;", q(table), q(c.Name), c.Def))
		goneNames = append(goneNames, c.Name)
	}
	if len(up) == 0 {
		return migrationSQL{}, nil
	}
	slices.Reverse(down)
	if len(addedNames) > 0 {
		parts = append(parts, "add "+strings.Join(addedNames, ", "))
	}
	if len(goneNames) > 0 {
		parts = append(parts, "drop "+strings.Join(goneNames, ", "))
	}
	return migrationSQL{
		Up:       strings.Join(up, "\n") + "\n",
		Down:     strings.Join(down, "\n") + "\n",
		Summary:  "ALTER TABLE " + table + ": " + strings.Join(parts, "; "),
		Warnings: warnings,
	}, nil
}

// columnChange is the SQL that changes one column's type or NULL. down is
// in the order it must run.
type columnChange struct {
	up, down, parts []string
	warnings        []string // see typeRisk.warnings
}

// changeColumn returns the SQL that turns a column defined by oldDef into
// c: a new type when the kind of type changed, and NOT NULL added or
// dropped. Primary keys and columns of unknown kinds are left alone.
func changeColumn(table, name, oldDef string, c sqlColumn, mysql bool) columnChange {
	var ch columnChange
	oldType, newType := defType(oldDef), defType(c.Def)
	oldKind := typeKind(oldType)
	if c.kind == "" || oldKind == "" || isPrimaryKey(oldDef) || isPrimaryKey(c.Def) {
		return ch
	}
	oldNull, newNull := isNullable(oldDef), isNullable(c.Def)
	typeChanged := oldKind != c.kind
	if !typeChanged && oldNull == newNull {
		return ch
	}
	t, col := quoteIdent(table, mysql), quoteIdent(name, mysql)
	if typeChanged {
		ch.parts = append(ch.parts, fmt.Sprintf("%s %s to %s", name, oldType, newType))
	}
	switch {
	case !newNull && oldNull:
		ch.parts = append(ch.parts, name+" NOT NULL")
	case newNull && !oldNull:
		ch.parts = append(ch.parts, name+" NULL")
	}
	fill := func(def, kind string) string { // fills NULLs before NOT NULL
		return fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s IS NULL;", t, col, cmp(defDefault(def), kindZero(kind, mysql)), col)
	}
	warn, downWarn := "", ""
	if typeChanged {
		risk := typeChangeRisk(oldType, newType, mysql)
		warn = risk.comment(name, oldType, newType, mysql, stepUp)
		ch.warnings = risk.warnings(table+"."+name, oldType, newType, stepUp)
		downWarn = typeChangeRisk(newType, oldType, mysql).comment(name, newType, oldType, mysql, stepDown)
	}
	if mysql {
		up := warn
		if !newNull && oldNull {
			up += fill(c.Def, c.kind) + "\n"
		}
		ch.up = append(ch.up, up+fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s;", t, col, c.Def))
		down := downWarn
		if !oldNull && newNull {
			down += fill(oldDef, oldKind) + "\n"
		}
		ch.down = append(ch.down, down+fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s;", t, col, oldDef))
		return ch
	}
	// Postgres: one ALTER TABLE with an action per change. A default of the
	// old type cannot be cast, so it is dropped first and set again after.
	pg := func(fromDef, toDef, toType, toKind string, changeType bool) string {
		var acts []string
		if changeType && defDefault(fromDef) != "" {
			acts = append(acts, "ALTER COLUMN "+col+" DROP DEFAULT")
		}
		if changeType {
			acts = append(acts, fmt.Sprintf("ALTER COLUMN %s TYPE %s USING %s::%s", col, toType, col, strings.ToLower(toType)))
		}
		if d := defDefault(toDef); d != "" && (changeType || defDefault(fromDef) != d) {
			acts = append(acts, "ALTER COLUMN "+col+" SET DEFAULT "+d)
		}
		pre := ""
		switch {
		case !isNullable(toDef) && isNullable(fromDef):
			pre = fill(toDef, toKind) + "\n"
			acts = append(acts, "ALTER COLUMN "+col+" SET NOT NULL")
		case isNullable(toDef) && !isNullable(fromDef):
			acts = append(acts, "ALTER COLUMN "+col+" DROP NOT NULL")
		}
		return pre + fmt.Sprintf("ALTER TABLE %s %s;", t, strings.Join(acts, ", "))
	}
	if typeChanged && pgSerial[strings.ToUpper(oldType)] != "" {
		oldType = pgSerial[strings.ToUpper(oldType)]
	}
	ch.up = append(ch.up, warn+pg(oldDef, c.Def, newType, c.kind, typeChanged))
	ch.down = append(ch.down, downWarn+pg(c.Def, oldDef, oldType, oldKind, typeChanged))
	return ch
}

var pgSerial = map[string]string{"SERIAL": "INTEGER", "BIGSERIAL": "BIGINT", "SMALLSERIAL": "SMALLINT"}

// defWords splits a column definition into words, keeping parentheses and
// quotes together: NUMERIC(10, 2) NOT NULL gives [NUMERIC(10, 2) NOT NULL].
func defWords(def string) []string {
	var out []string
	depth, start := 0, -1
	var quote byte
	for i := 0; i <= len(def); i++ {
		if i == len(def) || quote == 0 && depth == 0 && (def[i] == ' ' || def[i] == '\t' || def[i] == '\n') {
			if start >= 0 {
				out = append(out, def[start:i])
				start = -1
			}
			continue
		}
		c := def[i]
		if start < 0 {
			start = i
		}
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		}
	}
	return out
}

// defStops end the type at the start of a column definition.
var defStops = map[string]bool{
	"NOT": true, "NULL": true, "DEFAULT": true, "PRIMARY": true, "UNIQUE": true, "REFERENCES": true, "CHECK": true,
	"AUTO_INCREMENT": true, "GENERATED": true, "COLLATE": true, "CONSTRAINT": true, "COMMENT": true, "ON": true,
}

// defType is the type at the start of a column definition, upper case.
func defType(def string) string {
	w := defWords(def)
	n := 0
	for n < len(w) {
		u := strings.ToUpper(w[n])
		if defStops[u] || u == "CHARACTER" && n+1 < len(w) && strings.EqualFold(w[n+1], "SET") || strings.HasPrefix(u, "CHARSET") {
			break
		}
		n++
	}
	return strings.ToUpper(strings.Join(w[:n], " "))
}

// defDefault returns the DEFAULT expression of a column definition, or "".
func defDefault(def string) string {
	w := defWords(def)
	for i := 0; i+1 < len(w); i++ {
		if strings.EqualFold(w[i], "DEFAULT") {
			return w[i+1]
		}
	}
	return ""
}

func isNullable(def string) bool {
	u := " " + strings.ToUpper(strings.Join(defWords(def), " ")) + " "
	return !strings.Contains(u, " NOT NULL ") && !isPrimaryKey(def)
}

func isPrimaryKey(def string) bool {
	return strings.Contains(strings.ToUpper(def), "PRIMARY KEY")
}

// typeKind groups SQL types by the Go values they hold, so a column is
// changed only when a field moves to another group: text, integer, float,
// bool, time, bytes or json. It returns "" for types it does not know.
func typeKind(typ string) string {
	u := strings.ToUpper(strings.TrimSpace(typ))
	base := u
	if i := strings.IndexByte(base, '('); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	base = strings.TrimSpace(strings.TrimSuffix(base, " UNSIGNED"))
	switch {
	case u == "TINYINT(1)" || base == "BOOLEAN" || base == "BOOL":
		return "bool"
	}
	switch base {
	case "TEXT", "VARCHAR", "CHAR", "CHARACTER", "CHARACTER VARYING", "CITEXT", "TINYTEXT", "MEDIUMTEXT", "LONGTEXT", "ENUM", "NVARCHAR", "NCHAR", "UUID":
		return "text"
	case "SMALLINT", "INT", "INTEGER", "BIGINT", "MEDIUMINT", "TINYINT", "INT2", "INT4", "INT8", "SERIAL", "BIGSERIAL", "SMALLSERIAL":
		return "integer"
	case "REAL", "FLOAT", "FLOAT4", "FLOAT8", "DOUBLE", "DOUBLE PRECISION", "NUMERIC", "DECIMAL":
		return "float"
	case "TIMESTAMP", "TIMESTAMPTZ", "TIMESTAMP WITH TIME ZONE", "TIMESTAMP WITHOUT TIME ZONE", "DATETIME", "DATE":
		return "time"
	case "BYTEA", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY":
		return "bytes"
	case "JSON", "JSONB":
		return "json"
	}
	return ""
}

// kindZero is the value that replaces NULL when a column becomes NOT NULL.
func kindZero(kind string, mysql bool) string {
	switch kind {
	case "text":
		return "''"
	case "integer", "float":
		return "0"
	case "bool":
		return "FALSE"
	case "time":
		if mysql {
			return "CURRENT_TIMESTAMP(6)"
		}
		return "now()"
	}
	return "NULL"
}

// entityColumns reads the columns of the entity struct the way sql.Repository
// maps them: the db tag, or the snake_case field name. Without a pk option,
// the id column is the primary key, set by the database.
func entityColumns(file, entity string, mysql bool) ([]sqlColumn, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	st := structType(f, entity)
	if st == nil {
		return nil, fmt.Errorf("%s: no struct %s", file, entity)
	}
	type field struct {
		col, goType                          string
		pk, auto, created, updated, optional bool
	}
	var fields []field
	hasPK := false
	for _, fl := range st.Fields.List {
		if len(fl.Names) == 0 {
			continue // embedded: not followed here
		}
		goType := string(src[fset.Position(fl.Type.Pos()).Offset:fset.Position(fl.Type.End()).Offset])
		var tag reflect.StructTag
		if fl.Tag != nil {
			if s, err := strconv.Unquote(fl.Tag.Value); err == nil {
				tag = reflect.StructTag(s)
			}
		}
		dbName, dbOpts, _ := strings.Cut(tag.Get("db"), ",")
		_, jsonOpts, _ := strings.Cut(tag.Get("json"), ",")
		if dbName == "-" {
			continue
		}
		for _, n := range fl.Names {
			if !ast.IsExported(n.Name) {
				continue
			}
			fd := field{col: cmp(dbName, snakeCase(n.Name)), goType: goType, optional: slices.Contains(strings.Split(jsonOpts, ","), "omitempty")}
			for _, o := range strings.Split(dbOpts, ",") {
				switch strings.TrimSpace(o) {
				case "pk":
					fd.pk, hasPK = true, true
				case "auto":
					fd.auto = true
				case "created":
					fd.created = true
				case "updated":
					fd.updated = true
				}
			}
			fields = append(fields, fd)
		}
	}
	var out []sqlColumn
	for _, fd := range fields {
		if !hasPK && fd.col == "id" {
			fd.pk, fd.auto = true, true
		}
		typ, null, zero, known := sqlType(fd.goType, mysql)
		c := sqlColumn{Name: fd.col}
		if known {
			c.kind = typeKind(typ)
		} else {
			c.Note = "check the type: Go type " + fd.goType
		}
		switch {
		case fd.pk && fd.auto && isIntType(typ):
			if mysql {
				c.Def = typ + " AUTO_INCREMENT PRIMARY KEY"
			} else if typ == "INTEGER" || typ == "SMALLINT" {
				c.Def = "SERIAL PRIMARY KEY"
			} else {
				c.Def = "BIGSERIAL PRIMARY KEY"
			}
		case fd.pk:
			c.Def = typ + " PRIMARY KEY"
		case null:
			c.Def = typ
		default:
			c.Def = typ + " NOT NULL"
			isTime := strings.HasPrefix(zero, "now") || strings.HasPrefix(zero, "CURRENT")
			if zero != "" && (fd.created || fd.updated || fd.optional && !isTime) {
				c.Def += " DEFAULT " + zero
			} else {
				c.zero = zero
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func isIntType(typ string) bool {
	return typ == "BIGINT" || typ == "INTEGER" || typ == "INT" || typ == "SMALLINT"
}

// sqlNull maps the database/sql null types to the Go type they hold.
var sqlNull = map[string]string{
	"sql.NullString": "string", "sql.NullInt64": "int64", "sql.NullInt32": "int32", "sql.NullInt16": "int16",
	"sql.NullByte": "uint8", "sql.NullFloat64": "float64", "sql.NullBool": "bool", "sql.NullTime": "time.Time",
}

// sqlType returns the column type for a Go type, whether it is nullable,
// its zero value as an SQL default ("" when there is none), and whether the
// Go type is one it knows.
func sqlType(goType string, mysql bool) (typ string, null bool, zero string, known bool) {
	if t, ok := strings.CutPrefix(goType, "*"); ok {
		goType, null = t, true
	}
	if t, ok := sqlNull[goType]; ok {
		goType, null = t, true
	}
	pick := func(pg, my string) string {
		if mysql {
			return my
		}
		return pg
	}
	known = true
	switch goType {
	case "string":
		typ, zero = pick("TEXT", "VARCHAR(255)"), "''"
	case "int", "int64", "uint", "uint64", "uint32":
		typ, zero = "BIGINT", "0"
	case "int32", "uint16":
		typ, zero = pick("INTEGER", "INT"), "0"
	case "int16", "int8", "uint8", "byte":
		typ, zero = "SMALLINT", "0"
	case "float64":
		typ, zero = pick("DOUBLE PRECISION", "DOUBLE"), "0"
	case "float32":
		typ, zero = pick("REAL", "FLOAT"), "0"
	case "bool":
		typ, zero = "BOOLEAN", "FALSE"
	case "time.Time":
		typ, zero = pick("TIMESTAMPTZ", "DATETIME(6)"), pick("now()", "CURRENT_TIMESTAMP(6)")
	case "[]byte":
		typ = pick("BYTEA", "BLOB")
	case "json.RawMessage":
		typ = pick("JSONB", "JSON")
	default:
		typ, known = "TEXT", false
	}
	return typ, null, zero, known
}

// snakeCase is the column name sql.Repository gives a field without a db
// tag: UserID becomes user_id. It must match snake in sql/scan.go.
func snakeCase(s string) string {
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

// sqlKeywords are the column names that must be quoted.
var sqlKeywords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`all and any as asc between both by case check column constraint create
		cross current_date current_time current_timestamp current_user default desc distinct do else end
		except false fetch for foreign from grant group having in index inner insert interval into is join
		key keys leading left like limit natural not null offset on only or order outer primary range
		references right row rows select session_user table then to trailing true union unique user using
		values when where window with`) {
		sqlKeywords[w] = true
	}
}

// quoteIdent quotes a name that is a keyword or not a plain lower case
// identifier.
func quoteIdent(name string, mysql bool) string {
	plain := name != "" && !sqlKeywords[strings.ToLower(name)] && (name[0] < '0' || name[0] > '9') &&
		strings.IndexFunc(name, func(r rune) bool { return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' }) < 0
	switch {
	case plain:
		return name
	case mysql:
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// migrationHint tells how to update the table after an entity change, when
// the project has migrations.
func migrationHint(proj project, stdout io.Writer) {
	if _, err := os.Stat(filepath.Join(migrationsDir(proj), "embed.go")); err != nil {
		return
	}
	fmt.Fprintln(stdout, "For the table: dygo g migration")
}

// repoTables finds the tables the project gives sql.ForFeature[E] and
// sql.NewRepository[E], by entity name.
func repoTables(projDir string) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(projDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if path != projDir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			if src, err := os.ReadFile(path); err == nil {
				for _, m := range reRepoTable.FindAllSubmatch(src, -1) {
					out[string(m[1])] = string(m[2])
				}
			}
		}
		return nil
	})
	return out
}

// repoEntities finds the structs that are not in known but are given to
// sql.NewRepository[E] or sql.ForFeature[E] in the file that declares them,
// such as the users and refresh tokens of dygo g security.
func repoEntities(projDir string, known []entityPkg) []entityPkg {
	seen := map[string]bool{}
	for _, e := range known {
		seen[e.Dir+"\x00"+e.Entity] = true
	}
	var out []entityPkg
	_ = filepath.WalkDir(projDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if path != projDir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil || !reRepoTable.Match(src) {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}
		for _, m := range reRepoTable.FindAllSubmatch(src, -1) {
			name, dir := string(m[1]), filepath.Dir(path)
			if !seen[dir+"\x00"+name] && structType(f, name) != nil {
				seen[dir+"\x00"+name] = true
				out = append(out, entityPkg{Dir: dir, File: path, Entity: name})
			}
		}
		return nil
	})
	return out
}

var reRepoTable = regexp.MustCompile(`(?:ForFeature|NewRepository)\[(?:\w+\.)?(\w+)\]\((?:[^,()"]*,\s*)?"([^"]+)"`)

// tableName is the table of an entity: the one in repo (see repoTables), or
// else the plural snake_case name (OrderItem: order_items).
func tableName(repo map[string]string, entity string) string {
	if t, ok := repo[entity]; ok {
		return t
	}
	words := splitWords(entity)
	if len(words) == 0 {
		return strings.ToLower(entity)
	}
	words[len(words)-1] = plural(words[len(words)-1])
	return strings.Join(words, "_")
}

// migrationVerbs start a migration name; what follows names the table.
var migrationVerbs = map[string]bool{
	"create": true, "add": true, "update": true, "alter": true, "change": true, "modify": true, "remove": true, "drop": true,
}

// entityForMigration finds the entity a migration name is about:
// create_products, update_products and add_price_to_products all mean the
// entity whose table is products (or the entity Product).
func entityForMigration(entities []entityPkg, tables map[string]string, words []string) (entityPkg, bool) {
	var cands [][]string
	for i := len(words) - 1; i > 0; i-- {
		if w := words[i]; w == "to" || w == "from" || w == "in" || w == "on" || w == "of" || w == "for" {
			cands = append(cands, words[i+1:])
			break
		}
	}
	if len(words) > 1 && migrationVerbs[words[0]] {
		cands = append(cands, words[1:])
	}
	cands = append(cands, words)
	for _, c := range cands {
		if len(c) == 0 {
			continue
		}
		joined := strings.Join(c, "_")
		single := append([]string(nil), c...)
		single[len(single)-1] = singular(single[len(single)-1])
		for _, e := range entities {
			if strings.EqualFold(tables[e.Entity], joined) || e.Entity == pascal(single) {
				return e, true
			}
		}
	}
	return entityPkg{}, false
}

// projectDialect reports whether the project uses MySQL: from DATABASE_URL in
// .env or .env.example, or else from the driver package it imports. known is
// false when neither says.
func projectDialect(proj project) (mysql, known bool) {
	for _, env := range []string{".env", ".env.example"} {
		if v, ok := envValue(filepath.Join(proj.Dir, env), "DATABASE_URL"); ok {
			switch {
			case strings.HasPrefix(v, "mysql:"):
				return true, true
			case strings.HasPrefix(v, "postgres:"), strings.HasPrefix(v, "postgresql:"):
				return false, true
			}
		}
	}
	_ = filepath.WalkDir(proj.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || known {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if path != proj.Dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			if src, err := os.ReadFile(path); err == nil {
				switch {
				case strings.Contains(string(src), `/sql/mysql"`):
					mysql, known = true, true
				case strings.Contains(string(src), `/sql/postgres"`):
					known = true
				}
			}
		}
		return nil
	})
	return mysql, known
}

// replayMigrations returns the tables, by lower case name, that the up
// files in dir build, applied in version order.
func replayMigrations(dir string) (map[string]*tableState, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	type up struct {
		version int
		path    string
	}
	var ups []up
	for _, e := range entries {
		m := migrationFile.FindStringSubmatch(e.Name())
		if m == nil || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		ups = append(ups, up{v, filepath.Join(dir, e.Name())})
	}
	slices.SortFunc(ups, func(a, b up) int { return a.version - b.version })
	tables := map[string]*tableState{}
	for _, u := range ups {
		src, err := os.ReadFile(u.path)
		if err != nil {
			return nil, err
		}
		for _, stmt := range splitSQL(string(src)) {
			applyDDL(tables, stmt)
		}
	}
	return tables, nil
}

const identPat = "(?:[\\w$]+|\"[^\"]+\"|`[^`]+`)"

var (
	reCreateTable = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + identPat + `(?:\.` + identPat + `)*)\s*\((.*)\)`)
	reAlterTable  = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?(` + identPat + `(?:\.` + identPat + `)*)\s+(.*)$`)
	reDropTable   = regexp.MustCompile(`(?is)^DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?(.*)$`)
	reRenameTable = regexp.MustCompile(`(?is)^RENAME\s+TABLE\s+(.*)$`)
	reAddColumn   = regexp.MustCompile(`(?is)^ADD\s+(?:COLUMN\s+)?(?:IF\s+NOT\s+EXISTS\s+)?(` + identPat + `)\s+(.*)$`)
	reDropColumn  = regexp.MustCompile(`(?is)^DROP\s+(?:COLUMN\s+)?(?:IF\s+EXISTS\s+)?(` + identPat + `)`)
	reRenameCol   = regexp.MustCompile(`(?is)^RENAME\s+(?:COLUMN\s+)?(` + identPat + `)\s+TO\s+(` + identPat + `)$`)
	reRenameTo    = regexp.MustCompile(`(?is)^RENAME\s+(?:TO|AS)\s+(` + identPat + `(?:\.` + identPat + `)*)$`)
	reChangeCol   = regexp.MustCompile(`(?is)^CHANGE\s+(?:COLUMN\s+)?(` + identPat + `)\s+(` + identPat + `)\s+(.*)$`)
	reAlterCol    = regexp.MustCompile(`(?is)^ALTER\s+(?:COLUMN\s+)?(` + identPat + `)\s+(.*)$`)
	reColType     = regexp.MustCompile(`(?is)^(?:SET\s+DATA\s+)?TYPE\s+(.*?)(?:\s+(?:USING|COLLATE)\s+.*)?$`)
	reModifyCol   = regexp.MustCompile(`(?is)^MODIFY\s+(?:COLUMN\s+)?(` + identPat + `)\s+(.*)$`)
	reFirstWord   = regexp.MustCompile(`(?s)^(` + identPat + `)\s*(.*)$`)
	reDollarTag   = regexp.MustCompile(`^\$[A-Za-z_]*\$`)
)

// notColumns start a table element or an ALTER action that is not a column.
var notColumns = map[string]bool{
	"constraint": true, "primary": true, "unique": true, "foreign": true, "check": true, "index": true,
	"key": true, "fulltext": true, "spatial": true, "exclude": true, "like": true, "column": true,
}

// applyDDL updates tables with one statement. Statements it does not know
// are skipped.
func applyDDL(tables map[string]*tableState, stmt string) {
	if m := reCreateTable.FindStringSubmatch(stmt); m != nil {
		t := &tableState{}
		for _, el := range splitTop(m[2]) {
			w := reFirstWord.FindStringSubmatch(el)
			if w == nil || notColumns[strings.ToLower(w[1])] {
				continue
			}
			t.Columns = append(t.Columns, sqlColumn{Name: unquoteIdent(w[1]), Def: strings.TrimSpace(w[2])})
		}
		tables[tableKey(m[1])] = t
		return
	}
	if m := reDropTable.FindStringSubmatch(stmt); m != nil {
		for _, name := range splitTop(m[1]) {
			name = strings.Fields(name + " ")[0]
			delete(tables, tableKey(name))
		}
		return
	}
	if m := reRenameTable.FindStringSubmatch(stmt); m != nil {
		for _, pair := range splitTop(m[1]) {
			f := strings.Fields(pair)
			if len(f) == 3 && strings.EqualFold(f[1], "to") {
				if t, ok := tables[tableKey(f[0])]; ok {
					delete(tables, tableKey(f[0]))
					tables[tableKey(f[2])] = t
				}
			}
		}
		return
	}
	m := reAlterTable.FindStringSubmatch(stmt)
	if m == nil {
		return
	}
	key := tableKey(m[1])
	t, ok := tables[key]
	if !ok {
		return
	}
	for _, action := range splitTop(m[2]) {
		w := reFirstWord.FindStringSubmatch(action)
		if w == nil {
			continue
		}
		second := ""
		if s := reFirstWord.FindStringSubmatch(w[2]); s != nil {
			second = strings.ToLower(s[1])
		}
		switch strings.ToLower(w[1]) {
		case "add":
			if notColumns[second] && second != "column" {
				continue
			}
			if a := reAddColumn.FindStringSubmatch(action); a != nil {
				if name := unquoteIdent(a[1]); t.index(name) < 0 {
					t.Columns = append(t.Columns, sqlColumn{Name: name, Def: strings.TrimSpace(a[2])})
				}
			}
		case "drop":
			if notColumns[second] && second != "column" {
				continue
			}
			if a := reDropColumn.FindStringSubmatch(action); a != nil {
				if i := t.index(unquoteIdent(a[1])); i >= 0 {
					t.Columns = slices.Delete(t.Columns, i, i+1)
				}
			}
		case "rename":
			if a := reRenameTo.FindStringSubmatch(action); a != nil {
				delete(tables, key)
				key = tableKey(a[1])
				tables[key] = t
			} else if a := reRenameCol.FindStringSubmatch(action); a != nil && (second == "column" || !notColumns[second]) {
				if i := t.index(unquoteIdent(a[1])); i >= 0 {
					t.Columns[i].Name = unquoteIdent(a[2])
				}
			}
		case "change":
			if a := reChangeCol.FindStringSubmatch(action); a != nil {
				if i := t.index(unquoteIdent(a[1])); i >= 0 {
					t.Columns[i] = sqlColumn{Name: unquoteIdent(a[2]), Def: strings.TrimSpace(a[3])}
				}
			}
		case "modify":
			if a := reModifyCol.FindStringSubmatch(action); a != nil {
				if i := t.index(unquoteIdent(a[1])); i >= 0 {
					t.Columns[i].Def = strings.TrimSpace(a[2])
				}
			}
		case "alter":
			if a := reAlterCol.FindStringSubmatch(action); a != nil && (second == "column" || !notColumns[second]) {
				if i := t.index(unquoteIdent(a[1])); i >= 0 {
					t.Columns[i].Def = alterDef(t.Columns[i].Def, strings.TrimSpace(a[2]))
				}
			}
		}
	}
}

// alterDef applies an ALTER COLUMN action (TYPE, SET or DROP NOT NULL, SET
// or DROP DEFAULT) to a column definition.
func alterDef(def, action string) string {
	w := defWords(def)
	n := len(defWords(defType(def)))                 // the words of the type
	without := func(drop func(i int) int) []string { // drop returns how many words to drop at i
		var out []string
		for i := 0; i < len(w); {
			if k := drop(i); k > 0 {
				i += k
				continue
			}
			out = append(out, w[i])
			i++
		}
		return out
	}
	noNull := func(i int) int {
		switch {
		case i+1 < len(w) && strings.EqualFold(w[i], "NOT") && strings.EqualFold(w[i+1], "NULL"):
			return 2
		case i >= n && strings.EqualFold(w[i], "NULL"):
			return 1
		}
		return 0
	}
	noDefault := func(i int) int {
		if strings.EqualFold(w[i], "DEFAULT") && i+1 < len(w) {
			return 2
		}
		return 0
	}
	u := strings.ToUpper(strings.Join(strings.Fields(action), " "))
	switch {
	case reColType.MatchString(action):
		typ := reColType.FindStringSubmatch(action)[1]
		w = append([]string{strings.TrimSpace(typ)}, w[n:]...)
	case u == "SET NOT NULL":
		w = append(without(noNull), "NOT", "NULL")
	case u == "DROP NOT NULL":
		w = without(noNull)
	case strings.HasPrefix(u, "SET DEFAULT "):
		w = append(without(noDefault), "DEFAULT", strings.TrimSpace(action[strings.Index(strings.ToUpper(action), "DEFAULT")+len("DEFAULT"):]))
	case u == "DROP DEFAULT":
		w = without(noDefault)
	}
	return strings.Join(w, " ")
}

// tableKey is the lower case table name without its schema.
func tableKey(name string) string {
	parts := splitTopOn(name, '.')
	return strings.ToLower(unquoteIdent(strings.TrimSpace(parts[len(parts)-1])))
}

func unquoteIdent(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '`' && s[len(s)-1] == '`') {
		return s[1 : len(s)-1]
	}
	return s
}

// splitTop splits s on the commas outside parentheses and quotes.
func splitTop(s string) []string { return splitTopOn(s, ',') }

func splitTopOn(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == sep && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	if rest := strings.TrimSpace(s[start:]); rest != "" || len(out) == 0 {
		out = append(out, rest)
	}
	return out
}

// splitSQL splits a migration file into statements, without comments. It
// knows quotes, -- and /* */ comments, and Postgres dollar quoting.
func splitSQL(src string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, s)
		}
		b.Reset()
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '-' && strings.HasPrefix(src[i:], "--"):
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				i = len(src)
			} else {
				i += j
			}
			b.WriteByte(' ')
		case c == '/' && strings.HasPrefix(src[i:], "/*"):
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				i = len(src)
			} else {
				i += j + 3
			}
			b.WriteByte(' ')
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(src) {
				if src[j] == c {
					if j+1 < len(src) && src[j+1] == c {
						j += 2
						continue
					}
					break
				}
				if src[j] == '\\' && c == '\'' {
					j++
				}
				j++
			}
			end := min(j+1, len(src))
			b.WriteString(src[i:end])
			i = end - 1
		case c == '$':
			tag := reDollarTag.FindString(src[i:])
			if tag == "" {
				b.WriteByte(c)
				continue
			}
			j := strings.Index(src[i+len(tag):], tag)
			end := len(src)
			if j >= 0 {
				end = i + len(tag) + j + len(tag)
			}
			b.WriteString(src[i:end])
			i = end - 1
		case c == ';':
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return out
}
