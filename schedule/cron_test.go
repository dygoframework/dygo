package schedule

import (
	"testing"
	"time"
)

func TestCronNext(t *testing.T) {
	utc := time.UTC
	from := time.Date(2026, 10, 31, 10, 7, 30, 0, utc) // a Saturday
	cases := []struct {
		expr string
		want string
	}{
		{"* * * * *", "2026-10-31T10:08:00Z"},
		{"*/15 * * * *", "2026-10-31T10:15:00Z"},
		{"0 3 * * *", "2026-11-01T03:00:00Z"},
		{"30 * * * * *", "2026-10-31T10:08:30Z"},
		{"45 * * * * *", "2026-10-31T10:07:45Z"},
		{"0 */5 * * * *", "2026-10-31T10:10:00Z"},
		{"0 9 * * MON-FRI", "2026-11-02T09:00:00Z"},
		{"0 0 1 JAN *", "2027-01-01T00:00:00Z"},
		{"0 0 * * 7", "2026-11-01T00:00:00Z"},
		{"0 12 15 * 1", "2026-11-02T12:00:00Z"}, // day 15 or a Monday
		{"5/20 * * * *", "2026-10-31T10:25:00Z"},
		{"0 8-10,14 * * *", "2026-10-31T14:00:00Z"},
		{"@hourly", "2026-10-31T11:00:00Z"},
		{"@monthly", "2026-11-01T00:00:00Z"},
		{"0 0 29 2 *", "2028-02-29T00:00:00Z"},
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr, utc)
		if err != nil {
			t.Errorf("%q: %v", c.expr, err)
			continue
		}
		if got := cr.Next(from).Format(time.RFC3339); got != c.want {
			t.Errorf("%q: next = %s, want %s", c.expr, got, c.want)
		}
	}
	if cr, _ := ParseCron("0 0 30 2 *", utc); !cr.Next(from).IsZero() {
		t.Error("February 30 should never match")
	}
}

func TestCronZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	cr, _ := ParseCron("0 9 * * *", ny)
	got := cr.Next(time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC))
	if got.UTC().Format(time.RFC3339) != "2026-11-01T14:00:00Z" { // after the DST change
		t.Errorf("next = %s", got.UTC())
	}
}

func TestCronErrors(t *testing.T) {
	for _, expr := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "*/0 * * * *", "5-1 * * * *", "x * * * *", "* * * * FOO"} {
		if _, err := ParseCron(expr, nil); err == nil {
			t.Errorf("%q: want an error", expr)
		}
	}
}
