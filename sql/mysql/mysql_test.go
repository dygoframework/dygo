package mysql_test

import (
	"errors"
	"fmt"
	"testing"

	driver "github.com/go-sql-driver/mysql"

	"github.com/dygoframework/dygo/sql"
	"github.com/dygoframework/dygo/sql/internal/suite"
	"github.com/dygoframework/dygo/sql/mysql"
)

func TestDSN(t *testing.T) {
	d := mysql.Dialect()
	cases := map[string]string{
		"mysql://u:p@db:3306/app":                     "u:p@tcp(db:3306)/app?clientFoundRows=true&parseTime=true",
		"mysql://u:p@db/app":                          "u:p@tcp(db:3306)/app?clientFoundRows=true&parseTime=true",
		"mysql://u:p%40ss@db/app?parseTime=false":     "u:p@ss@tcp(db:3306)/app?clientFoundRows=true",
		"mysql://root@127.0.0.1:3307/app?loc=UTC":     "root@tcp(127.0.0.1:3307)/app?clientFoundRows=true&parseTime=true",
		"u:p@tcp(db:3306)/app":                        "u:p@tcp(db:3306)/app?clientFoundRows=true&parseTime=true",
		"mysql://u:p@db/app?charset=utf8mb4&tls=true": "u:p@tcp(db:3306)/app?clientFoundRows=true&charset=utf8mb4&parseTime=true&tls=true",
		"mysql://u:p@db/app?clientFoundRows=false":    "u:p@tcp(db:3306)/app?parseTime=true",
	}
	for in, want := range cases {
		got, err := d.DSN(in)
		if err != nil || got != want {
			t.Errorf("DSN(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"mysql:///app", "u:p@tcp(db:3306"} {
		if _, err := d.DSN(bad); err == nil {
			t.Errorf("DSN(%q): want error", bad)
		}
	}
}

func TestClassify(t *testing.T) {
	d := mysql.Dialect()
	cases := []struct {
		number uint16
		msg    string
		kind   sql.Kind
		want   string
	}{
		{1062, "Duplicate entry 'a@b.c' for key 'users.email'", sql.KindDuplicate, "users.email"},
		{1451, "Cannot delete or update a parent row", sql.KindForeignKey, ""},
		{1452, "Cannot add or update a child row", sql.KindForeignKey, ""},
		{1048, "Column 'email' cannot be null", sql.KindConstraint, ""},
		{3819, "Check constraint 'c' is violated.", sql.KindConstraint, ""},
		{1213, "Deadlock found", sql.KindRetryable, ""},
		{1146, "Table doesn't exist", sql.KindOther, ""},
	}
	for _, c := range cases {
		err := fmt.Errorf("exec: %w", &driver.MySQLError{Number: c.number, Message: c.msg})
		kind, constraint := d.Classify(err)
		if kind != c.kind || constraint != c.want {
			t.Errorf("%d: kind %d constraint %q", c.number, kind, constraint)
		}
	}
	if kind, _ := d.Classify(errors.New("other")); kind != sql.KindOther {
		t.Error("plain error must be KindOther")
	}
}

func TestIntegration(t *testing.T) {
	db := suite.Open(t, mysql.Dialect(), "DYGO_TEST_MYSQL_URL")
	suite.Run(t, db,
		`CREATE TABLE %s (id BIGINT PRIMARY KEY, email VARCHAR(255) NOT NULL UNIQUE) ENGINE=InnoDB`,
		`CREATE TABLE %s (id BIGINT PRIMARY KEY, parent_id BIGINT NOT NULL, FOREIGN KEY (parent_id) REFERENCES %s (id)) ENGINE=InnoDB`)
	t.Run("repository", func(t *testing.T) {
		suite.RunRepository(t, db,
			`CREATE TABLE %s (id BIGINT AUTO_INCREMENT PRIMARY KEY, email VARCHAR(191) NOT NULL UNIQUE, name VARCHAR(191) NOT NULL DEFAULT '', created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL) ENGINE=InnoDB`)
	})
}
