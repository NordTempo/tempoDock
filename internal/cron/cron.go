// Package cron parses cron expressions and finds when they are due next
// (scheduled jobs, ADR 0054).
//
// An expression has the five standard fields, minute (0-59), hour (0-23),
// day of month (1-31), month (1-12 or jan-dec) and day of week (0-7 or
// sun-sat, 0 and 7 both Sunday), each `*`, a value, a range `a-b`, a list
// `a,b`, or any of those with a step `/n`. As in Vixie cron, a day matches
// when either day field does if both are restricted. The macros @yearly
// (@annually), @monthly, @weekly, @daily (@midnight) and @hourly stand for
// their usual expressions.
//
// Times are wall clock in a location: on a daylight saving change, a
// time that doesn't exist that day is skipped and one that happens twice
// is due once.
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed expression.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	// domAny and dowAny are fields written `*`: then only the other day
	// field decides.
	domAny, dowAny bool
}

// field is one position of an expression.
type field struct {
	name     string
	min, max int
	names    []string // names[i] is the value min+i
}

var fields = [5]field{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}},
	{name: "day of week", min: 0, max: 7, names: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}},
}

var macros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

// Parse parses an expression. Its errors say which field is wrong and
// why, for people.
func Parse(expr string) (Schedule, error) {
	expr = strings.TrimSpace(strings.ToLower(expr))
	if m, ok := macros[expr]; ok {
		expr = m
	}
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Schedule{}, fmt.Errorf("a schedule has 5 fields (minute hour day-of-month month day-of-week), e.g. \"0 3 * * *\"; got %d", len(parts))
	}
	var s Schedule
	dst := [5]*uint64{&s.minute, &s.hour, &s.dom, &s.month, &s.dow}
	for i, p := range parts {
		bits, err := fields[i].parse(p)
		if err != nil {
			return Schedule{}, err
		}
		*dst[i] = bits
	}
	s.domAny, s.dowAny = parts[2] == "*", parts[4] == "*"
	if s.dow&(1<<7) != 0 { // 7 is Sunday too
		s.dow |= 1
	}
	if !s.matchesSomeDay() {
		return Schedule{}, errors.New("that day of month never happens in the months given")
	}
	return s, nil
}

// parse turns one field into a bit set of its values.
func (f field) parse(p string) (uint64, error) {
	var bits uint64
	for item := range strings.SplitSeq(p, ",") {
		rng, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			// A step past the field's values would only ever pick the
			// first one, and one near the largest int would overflow the
			// loop below.
			most := f.max - f.min + 1
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 || n > most {
				return 0, fmt.Errorf("%s: step %q must be a number from 1 to %d", f.name, stepText, most)
			}
			step = n
		}
		lo, hi := f.min, f.max
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if lo, err = f.value(a); err != nil {
				return 0, err
			}
			if hi, err = f.value(b); err != nil {
				return 0, err
			}
			if lo > hi {
				return 0, fmt.Errorf("%s: range %q runs backwards", f.name, rng)
			}
		default:
			v, err := f.value(rng)
			if err != nil {
				return 0, err
			}
			lo = v
			if !hasStep {
				hi = v
			}
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << v
		}
	}
	return bits, nil
}

// value reads one number or name of the field.
func (f field) value(s string) (int, error) {
	for i, n := range f.names {
		if s == n {
			return f.min + i, nil
		}
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

// matchesSomeDay reports whether a day-of-month restriction can ever be
// met by the months (31 in February never is), unless the weekday can.
func (s Schedule) matchesSomeDay() bool {
	if !s.dowAny && !s.domAny || s.domAny {
		return true
	}
	days := [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	for m := 1; m <= 12; m++ {
		if s.month&(1<<m) == 0 {
			continue
		}
		for d := 1; d <= days[m]; d++ {
			if s.dom&(1<<d) != 0 {
				return true
			}
		}
	}
	return false
}

// dayMatches reports whether t's day is one the schedule runs on.
func (s Schedule) dayMatches(t time.Time) bool {
	dom := s.dom&(1<<t.Day()) != 0
	dow := s.dow&(1<<int(t.Weekday())) != 0
	switch {
	case s.domAny && s.dowAny:
		return true
	case s.domAny:
		return dow
	case s.dowAny:
		return dom
	}
	return dom || dow
}

// maxYears bounds Next's search; an expression that parses matches at
// least once every few years (29 February every 4, with a leap year
// skipped at most once in 8).
const maxYears = 9

// Next returns the first time after t (to the minute, in t's location)
// the schedule is due.
func (s Schedule) Next(t time.Time) time.Time {
	loc := t.Location()
	t = t.Truncate(time.Minute)
	// Due times only move forward on the wall clock: when it falls back
	// an hour, the repeated times were due already.
	from := wall(t)
	t = t.Add(time.Minute)
	limit := t.Year() + maxYears
	for t.Year() <= limit {
		y, mo, d := t.Date()
		switch {
		case s.month&(1<<int(mo)) == 0:
			t = time.Date(y, mo+1, 1, 0, 0, 0, 0, loc)
		case !s.dayMatches(t):
			t = time.Date(y, mo, d+1, 0, 0, 0, 0, loc)
		case s.hour&(1<<t.Hour()) == 0:
			next := time.Date(y, mo, d, t.Hour()+1, 0, 0, 0, loc)
			if !next.After(t) { // a repeated hour: move past it
				next = t.Add(time.Hour).Truncate(time.Hour)
			}
			t = next
		case s.minute&(1<<t.Minute()) == 0 || !wall(t).After(from):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// wall is t's wall clock reading, to compare across a clock change.
func wall(t time.Time) time.Time {
	y, mo, d := t.Date()
	return time.Date(y, mo, d, t.Hour(), t.Minute(), 0, 0, time.UTC)
}
