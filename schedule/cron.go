package schedule

import (
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed cron expression.
type Cron struct {
	expr                string
	sec, min, hour, dom uint64
	month, dow          uint64
	domStar, dowStar    bool
	loc                 *time.Location
}

// String returns the expression as written.
func (c *Cron) String() string { return c.expr }

type field struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	fSec   = field{name: "second", min: 0, max: 59}
	fMin   = field{name: "minute", min: 0, max: 59}
	fHour  = field{name: "hour", min: 0, max: 23}
	fDom   = field{name: "day of month", min: 1, max: 31}
	fMonth = field{name: "month", min: 1, max: 12, names: map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}}
	fDow = field{name: "day of week", min: 0, max: 7, names: map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}}
)

var macros = map[string]string{
	"@yearly":   "0 0 0 1 1 *",
	"@annually": "0 0 0 1 1 *",
	"@monthly":  "0 0 0 1 * *",
	"@weekly":   "0 0 0 * * 0",
	"@daily":    "0 0 0 * * *",
	"@midnight": "0 0 0 * * *",
	"@hourly":   "0 0 * * * *",
}

// ParseCron parses a cron expression with five fields (minute hour
// day-of-month month day-of-week) or six (with seconds first). Fields take *,
// lists (1,2), ranges (1-5), steps (*/15, 0-30/5) and English month and day
// names (JAN, MON). A macro (@hourly, @daily, @weekly, @monthly, @yearly) can
// replace the fields. As in standard cron, when both day fields are
// restricted a day matches either one. Times are in loc, or time.Local when
// loc is nil.
func ParseCron(expr string, loc *time.Location) (*Cron, error) {
	if loc == nil {
		loc = time.Local
	}
	spec := strings.TrimSpace(expr)
	if m, ok := macros[strings.ToLower(spec)]; ok {
		spec = m
	}
	fs := strings.Fields(spec)
	switch len(fs) {
	case 5:
		fs = append([]string{"0"}, fs...)
	case 6:
	default:
		return nil, fmt.Errorf("schedule: cron %q: want 5 or 6 fields, got %d", expr, len(fs))
	}
	c := &Cron{expr: expr, loc: loc}
	var err error
	parse := func(dst *uint64, s string, f field) {
		if err == nil {
			*dst, err = parseField(s, f)
			if err != nil {
				err = fmt.Errorf("schedule: cron %q: %w", expr, err)
			}
		}
	}
	parse(&c.sec, fs[0], fSec)
	parse(&c.min, fs[1], fMin)
	parse(&c.hour, fs[2], fHour)
	parse(&c.dom, fs[3], fDom)
	parse(&c.month, fs[4], fMonth)
	parse(&c.dow, fs[5], fDow)
	if err != nil {
		return nil, err
	}
	if c.dow&(1<<7) != 0 { // 7 is Sunday too
		c.dow |= 1
	}
	c.domStar = fs[3] == "*" || fs[3] == "?"
	c.dowStar = fs[5] == "*" || fs[5] == "?"
	return c, nil
}

func parseField(s string, f field) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(s, ",") {
		lo, hi, step := f.min, f.max, 1
		rng := part
		if i := strings.IndexByte(part, '/'); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("%s: bad step in %q", f.name, part)
			}
			step, rng = n, part[:i]
		}
		switch {
		case rng == "*" || rng == "?":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if lo, err = f.value(a); err != nil {
				return 0, err
			}
			if hi, err = f.value(b); err != nil {
				return 0, err
			}
		default:
			v, err := f.value(rng)
			if err != nil {
				return 0, err
			}
			lo = v
			if strings.Contains(part, "/") {
				hi = f.max // 5/15 means from 5 to the end, every 15
			} else {
				hi = v
			}
		}
		if lo > hi {
			return 0, fmt.Errorf("%s: range %q goes backwards", f.name, part)
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << v
		}
	}
	return set, nil
}

func (f field) value(s string) (int, error) {
	if v, ok := f.names[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", f.name, s)
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("%s: %d is outside %d-%d", f.name, v, f.min, f.max)
	}
	return v, nil
}

// Next returns the first time after t that matches, or the zero time when
// none does within five years (for example February 30).
func (c *Cron) Next(t time.Time) time.Time {
	t = t.In(c.loc).Truncate(time.Second).Add(time.Second)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if c.month&(1<<int(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, c.loc)
			continue
		}
		if !c.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, c.loc)
			continue
		}
		if c.hour&(1<<t.Hour()) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, c.loc)
			continue
		}
		if c.min&(1<<t.Minute()) == 0 {
			t = t.Truncate(time.Minute).Add(time.Minute)
			continue
		}
		if c.sec&(1<<t.Second()) == 0 {
			// Jump to the next matching second in this minute, if any.
			rest := c.sec >> (t.Second() + 1)
			if rest == 0 {
				t = t.Truncate(time.Minute).Add(time.Minute)
			} else {
				t = t.Add(time.Duration(bits.TrailingZeros64(rest)+1) * time.Second)
			}
			continue
		}
		return t
	}
	return time.Time{}
}

func (c *Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<t.Day()) != 0
	dow := c.dow&(1<<int(t.Weekday())) != 0
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dow
	case c.dowStar:
		return dom
	default:
		return dom || dow
	}
}
