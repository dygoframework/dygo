package main

import (
	"fmt"
	"slices"
	"strings"
)

// safeMigration is dygo g migration -safe: an expand and contract change, so
// the old and the new code can run side by side during a rolling deploy.
//
// The expand migration adds what the new code needs and keeps everything
// the old code uses: a renamed column is added under its new name, filled
// from the old one, and kept in sync with it by a trigger in both
// directions. New fields are added and NULL changes are made here too. The
// contract migration (marked -- dygo:after-deploy, so dygo db migrate waits
// for --contract) removes the trigger and the old columns, once no running
// code reads them.
//
// A type change under the same column name cannot be safe, since old and new
// code would read one column as two types: it is refused with a hint to give
// the new column a new name.
func safeMigration(entity, table string, cols []sqlColumn, cur *tableState, renames []rename, added, gone []sqlColumn, mysql bool) (migrationSQL, error) {
	type step struct{ up, down string }
	var expand, contract []step
	var parts, warnings []string
	q := func(name string) string { return quoteIdent(name, mysql) }

	for _, c := range cols {
		i := cur.index(c.Name)
		if i < 0 {
			continue
		}
		oldDef := cur.Columns[i].Def
		if k := typeKind(defType(oldDef)); c.kind != "" && k != "" && k != c.kind && !isPrimaryKey(oldDef) && !isPrimaryKey(c.Def) {
			return migrationSQL{}, fmt.Errorf("-safe: %s.%s changes from %s to %s under the same name, which old and new code cannot share; "+
				"give the field a new column (for example db:\"%s_v2\") and run again with -rename %s:%s_v2",
				table, c.Name, defType(oldDef), defType(c.Def), c.Name, c.Name, c.Name)
		}
		ch := changeColumn(table, c.Name, oldDef, c, mysql)
		if len(ch.up) > 0 {
			expand = append(expand, step{strings.Join(ch.up, "\n"), strings.Join(ch.down, "\n")})
			parts = append(parts, ch.parts...)
			warnings = append(warnings, ch.warnings...)
		}
	}
	for _, c := range added {
		def := c.Def
		if strings.HasSuffix(def, " NOT NULL") && c.zero != "" {
			def += " DEFAULT " + c.zero
		}
		up := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;", q(table), q(c.Name), def)
		if c.Note != "" {
			up += " -- " + c.Note
		}
		expand = append(expand, step{up, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", q(table), q(c.Name))})
		parts = append(parts, "add "+c.Name)
	}
	for _, r := range renames {
		if isPrimaryKey(r.from.Def) || isPrimaryKey(r.to.Def) {
			return migrationSQL{}, fmt.Errorf("-safe cannot rename the primary key %s.%s", table, r.from.Name)
		}
		s := newSync(table, r.from, r.to, mysql)
		if s.typeChanged() {
			warnings = append(warnings, typeChangeRisk(s.fromT, s.toT, mysql).warnings(
				fmt.Sprintf("%s.%s (copied into %s)", table, r.from.Name, r.to.Name), s.fromT, s.toT, stepSafe)...)
		}
		expand = append(expand, step{s.expandUp(), s.expandDown()})
		contract = append(contract, step{s.contractUp(), s.contractDown()})
		parts = append(parts, fmt.Sprintf("rename %s to %s (synced until --contract)", r.from.Name, r.to.Name))
	}
	for _, c := range gone {
		up := fmt.Sprintf("-- %s is not a field of %s any more; the deployed code no longer reads it.\nALTER TABLE %s DROP COLUMN %s;", c.Name, entity, q(table), q(c.Name))
		contract = append(contract, step{up, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;", q(table), q(c.Name), c.Def)})
		parts = append(parts, "drop "+c.Name+" after the deploy")
	}
	if len(expand)+len(contract) == 0 {
		return migrationSQL{}, nil
	}
	join := func(steps []step) (up, down string) {
		var u, d []string
		for _, s := range steps {
			u = append(u, s.up)
			d = append(d, s.down)
		}
		slices.Reverse(d)
		if len(u) == 0 {
			return "", ""
		}
		return strings.Join(u, "\n\n") + "\n", strings.Join(d, "\n\n") + "\n"
	}
	out := migrationSQL{Summary: "ALTER TABLE " + table + ": " + strings.Join(parts, "; "), Warnings: warnings}
	out.Up, out.Down = join(expand)
	out.ContractUp, out.ContractDown = join(contract)
	if out.Up == "" { // only drops: the expand half has nothing to do
		out.Up = "-- Nothing to add: the contract migration drops the old columns after the deploy.\n"
	}
	return out, nil
}

// columnSync renames a column in two steps, with a trigger that copies
// writes between the old and the new column while both exist.
type columnSync struct {
	table, name     string // table, and the trigger (and function) name
	from, to        sqlColumn
	fromT, toT      string // their types
	fromNN, toNN    bool   // NOT NULL
	fromDef, toDef  string // their DEFAULT expressions, or ""
	mysql           bool
	qt, qfrom, qto  string // quoted names
	castTo, castFro string // ::type casts for Postgres, "" when the types match
}

func newSync(table string, from, to sqlColumn, mysql bool) columnSync {
	s := columnSync{
		table: table, from: from, to: to, mysql: mysql,
		fromT: defType(from.Def), toT: defType(to.Def),
		fromNN: !isNullable(from.Def), toNN: !isNullable(to.Def),
		fromDef: defDefault(from.Def), toDef: defDefault(to.Def),
		qt: quoteIdent(table, mysql), qfrom: quoteIdent(from.Name, mysql), qto: quoteIdent(to.Name, mysql),
	}
	s.name = "dygo_sync_" + table + "_" + from.Name
	if len(s.name) > 59 { // room for _ins/_upd within the 63/64 character limits
		s.name = s.name[:59]
	}
	if !mysql && !strings.EqualFold(s.fromT, s.toT) {
		s.castTo, s.castFro = "::"+strings.ToLower(s.toT), "::"+strings.ToLower(s.fromT)
	}
	return s
}

// typeChanged reports whether the copy converts the values to another kind.
func (s columnSync) typeChanged() bool {
	return typeKind(s.fromT) != typeKind(s.toT)
}

// risk is the comment on what the copy from one column into the other can
// do to the values; "" when the kinds match.
func (s columnSync) risk(from, to, fromT, toT string, step riskStep) string {
	if !s.typeChanged() {
		return ""
	}
	r := typeChangeRisk(fromT, toT, s.mysql)
	return strings.TrimSuffix(r.comment(from+" into "+to, fromT, toT, s.mysql, step), "\n")
}

func (s columnSync) alter(format string, args ...any) string {
	return fmt.Sprintf("ALTER TABLE %s ", s.qt) + fmt.Sprintf(format, args...) + ";"
}

// copyInto fills column dst from src for the existing rows.
func (s columnSync) copyInto(dst, src, cast string, notNull bool, kind string) string {
	val := src + cast
	if notNull {
		val = fmt.Sprintf("COALESCE(%s, %s)", val, kindZero(kind, s.mysql))
	}
	return fmt.Sprintf("UPDATE %s SET %s = %s;", s.qt, dst, val)
}

// setNotNull makes a column NOT NULL without a default.
func (s columnSync) setNotNull(col, typ string) string {
	if s.mysql {
		return s.alter("MODIFY COLUMN %s %s NOT NULL", col, typ)
	}
	return s.alter("ALTER COLUMN %s SET NOT NULL", col)
}

func (s columnSync) triggers() string {
	if s.mysql {
		return fmt.Sprintf("CREATE TRIGGER %s_ins BEFORE INSERT ON %s FOR EACH ROW SET NEW.%s = COALESCE(NEW.%s, NEW.%s), NEW.%s = COALESCE(NEW.%s, NEW.%s);\n",
			s.name, s.qt, s.qto, s.qto, s.qfrom, s.qfrom, s.qfrom, s.qto) +
			fmt.Sprintf("CREATE TRIGGER %s_upd BEFORE UPDATE ON %s FOR EACH ROW SET NEW.%s = IF(NEW.%s <=> OLD.%s, NEW.%s, NEW.%s), NEW.%s = NEW.%s;",
				s.name, s.qt, s.qto, s.qfrom, s.qfrom, s.qto, s.qfrom, s.qfrom, s.qto)
	}
	return fmt.Sprintf(`CREATE FUNCTION %[1]s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.%[2]s IS NULL THEN NEW.%[2]s := NEW.%[3]s%[4]s; END IF;
    IF NEW.%[3]s IS NULL THEN NEW.%[3]s := NEW.%[2]s%[5]s; END IF;
  ELSIF NEW.%[3]s IS DISTINCT FROM OLD.%[3]s THEN
    NEW.%[2]s := NEW.%[3]s%[4]s;
  ELSE
    NEW.%[3]s := NEW.%[2]s%[5]s;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER %[1]s BEFORE INSERT OR UPDATE ON %[6]s FOR EACH ROW EXECUTE FUNCTION %[1]s();`,
		s.name, s.qto, s.qfrom, s.castTo, s.castFro, s.qt)
}

func (s columnSync) dropTriggers() string {
	if s.mysql {
		return fmt.Sprintf("DROP TRIGGER %s_upd;\nDROP TRIGGER %s_ins;", s.name, s.name)
	}
	return fmt.Sprintf("DROP TRIGGER %s ON %s;\nDROP FUNCTION %s();", s.name, s.qt, s.name)
}

// expandUp adds the new column next to the old one, copies the data and
// starts the sync. The old column loses its default meanwhile, so a row
// the new code inserts reaches the trigger with NULL there and gets the new
// column's value.
func (s columnSync) expandUp() string {
	lines := []string{
		fmt.Sprintf("-- Expand: %s.%s becomes %s. A trigger copies writes between the two columns until the\n-- contract migration, so the old and the new code can run side by side.", s.table, s.from.Name, s.to.Name),
		s.alter("ADD COLUMN %s %s", s.qto, s.toT),
	}
	if c := s.risk(s.from.Name, s.to.Name, s.fromT, s.toT, stepSafe); c != "" {
		lines = append(lines, c)
	}
	lines = append(lines, s.copyInto(s.qto, s.qfrom, s.castTo, s.toNN, s.to.kind))
	if s.fromDef != "" {
		lines = append(lines, s.alter("ALTER COLUMN %s DROP DEFAULT", s.qfrom))
	}
	lines = append(lines, s.triggers())
	if s.toNN {
		lines = append(lines, s.setNotNull(s.qto, s.toT))
	}
	return strings.Join(lines, "\n")
}

func (s columnSync) expandDown() string {
	lines := []string{s.dropTriggers()}
	if s.fromDef != "" {
		lines = append(lines, s.alter("ALTER COLUMN %s SET DEFAULT %s", s.qfrom, s.fromDef))
	}
	lines = append(lines, s.alter("DROP COLUMN %s", s.qto))
	return strings.Join(lines, "\n")
}

// contractUp ends the sync and drops the old column.
func (s columnSync) contractUp() string {
	lines := []string{
		fmt.Sprintf("-- Contract: the deployed code uses %s.%s; stop the sync and drop %s.", s.table, s.to.Name, s.from.Name),
		s.dropTriggers(),
		s.alter("DROP COLUMN %s", s.qfrom),
	}
	if s.toDef != "" {
		lines = append(lines, s.alter("ALTER COLUMN %s SET DEFAULT %s", s.qto, s.toDef))
	}
	return strings.Join(lines, "\n")
}

// contractDown brings back the old column and the sync.
func (s columnSync) contractDown() string {
	var lines []string
	if s.toDef != "" {
		lines = append(lines, s.alter("ALTER COLUMN %s DROP DEFAULT", s.qto))
	}
	lines = append(lines, s.alter("ADD COLUMN %s %s", s.qfrom, s.fromT))
	if c := s.risk(s.to.Name, s.from.Name, s.toT, s.fromT, stepDown); c != "" {
		lines = append(lines, c)
	}
	lines = append(lines, s.copyInto(s.qfrom, s.qto, s.castFro, s.fromNN, typeKind(s.fromT)))
	if s.fromNN {
		lines = append(lines, s.setNotNull(s.qfrom, s.fromT))
	}
	lines = append(lines, s.triggers())
	return strings.Join(lines, "\n")
}
