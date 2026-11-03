// Package mysql is the MySQL dialect for the Dygo sql module, on
// go-sql-driver/mysql:
//
//	Imports: []*dygo.Module{mysql.Module()} // reads DATABASE_URL
//
// DATABASE_URL is a mysql://user:pass@host:3306/db?param=value URL or the
// driver's own DSN (user:pass@tcp(host:3306)/db). parseTime=true is added
// unless the URL sets it, so DATETIME columns scan into time.Time, and so is
// clientFoundRows=true, so an UPDATE reports the rows it matched rather than
// only those it changed (an update with unchanged values is not "not found").
package mysql

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	driver "github.com/go-sql-driver/mysql"

	"github.com/dygoframework/dygo"
	"github.com/dygoframework/dygo/sql"
)

// Module returns sql.Module with the MySQL dialect.
func Module(opts ...sql.Option) *dygo.Module { return sql.Module(Dialect(), opts...) }

// Dialect returns the MySQL dialect, for sql.Open and tests.
func Dialect() sql.Dialect { return dialect{} }

type dialect struct{}

func (dialect) Name() string       { return "mysql" }
func (dialect) DriverName() string { return "mysql" }

// DSN turns a mysql:// URL into the driver's DSN and checks it.
func (dialect) DSN(raw string) (string, error) {
	dsn := raw
	if strings.HasPrefix(raw, "mysql://") {
		var err error
		if dsn, err = fromURL(raw); err != nil {
			return "", err
		}
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		return "", err
	}
	if !hasParam(dsn, "parseTime") {
		cfg.ParseTime = true
	}
	if !hasParam(dsn, "clientFoundRows") {
		cfg.ClientFoundRows = true
	}
	return cfg.FormatDSN(), nil
}

func fromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", errors.New("mysql URL has no host")
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "3306")
	}
	var auth string
	if u.User != nil {
		auth = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			auth += ":" + pw
		}
		auth += "@"
	}
	dsn := fmt.Sprintf("%stcp(%s)/%s", auth, host, strings.TrimPrefix(u.Path, "/"))
	if u.RawQuery != "" {
		dsn += "?" + u.RawQuery
	}
	return dsn, nil
}

func hasParam(dsn, name string) bool {
	i := strings.LastIndex(dsn, "?")
	if i < 0 {
		return false
	}
	q, err := url.ParseQuery(dsn[i+1:])
	return err == nil && q.Has(name)
}

// keyName pulls the key out of "Duplicate entry 'x' for key 'users.email'".
var keyName = regexp.MustCompile(`for key '([^']+)'`)

// Classify maps MySQL error numbers.
func (dialect) Classify(err error) (sql.Kind, string) {
	var me *driver.MySQLError
	if !errors.As(err, &me) {
		return sql.KindOther, ""
	}
	switch me.Number {
	case 1062: // ER_DUP_ENTRY
		key := ""
		if m := keyName.FindStringSubmatch(me.Message); m != nil {
			key = m[1]
		}
		return sql.KindDuplicate, key
	case 1451, 1452: // ER_ROW_IS_REFERENCED_2, ER_NO_REFERENCED_ROW_2
		return sql.KindForeignKey, ""
	case 1048, 3819: // ER_BAD_NULL_ERROR, ER_CHECK_CONSTRAINT_VIOLATED
		return sql.KindConstraint, ""
	case 1213: // ER_LOCK_DEADLOCK
		return sql.KindRetryable, ""
	case 1146, 1054: // ER_NO_SUCH_TABLE, ER_BAD_FIELD_ERROR
		return sql.KindSchema, ""
	}
	return sql.KindOther, ""
}

func (dialect) Placeholder(int) string { return "?" }

// BackslashEscapes is true: MySQL treats \' inside '...' as an escaped quote
// unless NO_BACKSLASH_ESCAPES is set.
func (dialect) BackslashEscapes() bool { return true }

// Quote quotes an identifier with backticks, part by part for a dotted name.
func (dialect) Quote(ident string) string {
	parts := strings.Split(ident, ".")
	for i, p := range parts {
		parts[i] = "`" + strings.ReplaceAll(p, "`", "``") + "`"
	}
	return strings.Join(parts, ".")
}

// Returning is false: MySQL has no INSERT ... RETURNING, so the repository
// reads the auto increment column from LastInsertId.
func (dialect) Returning() bool { return false }
