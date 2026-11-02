package main

import (
	"fmt"
	"strconv"
	"strings"
)

// typeRisk is what changing a column from one type to another can do to
// the rows already in it. dygo g migration writes it above the statement
// and prints it, so a lossy change is never a surprise.
type typeRisk struct {
	Lost string // how values change for good (rounded, cut); "" when none
	Fail string // which values make the change fail; "" when all convert
}

// typeChangeRisk says what converting the values of a from column to the
// to type can do. It knows the kinds of typeKind; any other pair is
// reported as possibly failing.
func typeChangeRisk(from, to string, mysql bool) typeRisk {
	fk, tk := typeKind(from), typeKind(to)
	var r typeRisk
	switch {
	case fk == tk:
	case fk == "float" && tk == "integer":
		r.Lost = "decimals are rounded (2.7 becomes 3)"
		r.Fail = "numbers too big for " + to
	case fk == "integer" && tk == "float":
		switch b := typeBase(to); b {
		case "REAL", "FLOAT", "FLOAT4", "FLOAT8", "DOUBLE", "DOUBLE PRECISION":
			r.Lost = "large integers lose precision in " + b
		default: // NUMERIC and DECIMAL are exact
			if strings.Contains(to, "(") {
				r.Fail = "numbers too big for " + to
			}
		}
	case (fk == "integer" || fk == "float") && tk == "bool":
		switch {
		case mysql: // BOOLEAN is TINYINT(1): values are kept, if they fit
			r.Fail = "numbers outside -128 to 127"
			if fk == "float" {
				r.Lost = "decimals are rounded"
			}
		case fk == "float":
			r.Fail = "every row: Postgres has no cast from " + from + " to " + to
		default:
			r.Lost = "every number other than 0 becomes true (2 and -1 read back as 1)"
		}
	case fk == "bool" && tk == "integer":
	case fk == "bool" && tk == "float" && !mysql:
		r.Fail = "every row: Postgres has no cast from " + from + " to " + to
	case fk == "bytes" && tk == "text":
		if mysql {
			r.Fail = "bytes that are not valid text in the column's character set"
		} else {
			r.Lost = "bytes become hex text such as \\x6869, not the decoded text"
		}
	case tk == "text":
	case fk == "text":
		r.Fail = "text that is not a valid " + to + " value"
	default:
		r.Fail = "values that do not convert from " + from + " to " + to
	}
	if fk != "text" && tk == "text" {
		if n := typeLength(to); n > 0 {
			if mysql {
				r.Fail = fmt.Sprintf("values longer than %d characters", n)
			} else {
				r.Lost = fmt.Sprintf("values longer than %d characters are cut", n)
			}
		}
	}
	return r
}

// typeBase is a type without its length and UNSIGNED: VARCHAR(20) gives
// VARCHAR.
func typeBase(typ string) string {
	u := strings.ToUpper(strings.TrimSpace(typ))
	if i := strings.IndexByte(u, '('); i >= 0 {
		u = strings.TrimSpace(u[:i])
	}
	return strings.TrimSpace(strings.TrimSuffix(u, " UNSIGNED"))
}

// typeLength is n in VARCHAR(n) or CHAR(n), or 0.
func typeLength(typ string) int {
	switch typeBase(typ) {
	case "VARCHAR", "CHAR", "CHARACTER", "CHARACTER VARYING", "NVARCHAR", "NCHAR":
	default:
		return 0
	}
	_, rest, ok := strings.Cut(typ, "(")
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), ")")))
	return n
}

// riskStep is where a type change runs, which changes what can be undone.
type riskStep int

const (
	stepUp   riskStep = iota // an up migration: the down one cannot restore lost values
	stepDown                 // a down migration, undoing a type change
	stepSafe                 // -safe: the old column keeps the values until --contract
)

// comment is the SQL comment written above the statement that converts the
// values.
func (r typeRisk) comment(col, from, to string, mysql bool, step riskStep) string {
	var b strings.Builder
	fmt.Fprintf(&b, "-- %s changes from %s to %s", col, from, to)
	if r.Lost == "" && r.Fail == "" {
		b.WriteString("; every value converts.\n")
		return b.String()
	}
	b.WriteString(".\n")
	if r.Lost != "" {
		fmt.Fprintf(&b, "-- DATA LOSS: %s", r.Lost)
		switch step {
		case stepUp:
			b.WriteString("; the down migration cannot bring them back. Back up the table first.\n")
		case stepDown:
			b.WriteString(", for the values written since the up migration.\n")
		case stepSafe:
			b.WriteString(". The old column keeps the original values until --contract.\n")
		}
	}
	if r.Fail != "" {
		fmt.Fprintf(&b, "-- MAY FAIL on %s.", r.Fail)
		switch {
		case step == stepSafe:
			b.WriteString(" While the sync runs, writes of such values fail too.")
		case mysql:
			b.WriteString(" MySQL cannot roll back a schema change: check the data first.")
		default:
			b.WriteString(" Postgres then rolls the migration back.")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// warnings are the lines dygo g migration prints for an up change.
func (r typeRisk) warnings(col, from, to string, step riskStep) []string {
	var out []string
	if r.Lost != "" {
		w := fmt.Sprintf("%s %s to %s loses data: %s.", col, from, to, r.Lost)
		if step == stepSafe {
			w += " The old column keeps the original values until --contract."
		} else {
			w += " The down migration cannot bring them back: back up the table first."
		}
		out = append(out, w)
	}
	if r.Fail != "" {
		w := fmt.Sprintf("%s %s to %s fails on %s: check the data first.", col, from, to, r.Fail)
		if step == stepSafe {
			w = strings.TrimSuffix(w, ".") + "; while the sync runs, writes of such values fail too."
		}
		out = append(out, w)
	}
	return out
}
