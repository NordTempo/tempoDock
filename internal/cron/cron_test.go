package cron

import (
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// TestNext walks expressions from a fixed time and checks the next due
// times, across months, weekdays, steps and names.
func TestNext(t *testing.T) {
	from := time.Date(2026, 11, 4, 10, 17, 30, 0, time.UTC) // a Wednesday
	for _, tc := range []struct {
		expr string
		want []string
	}{
		{"* * * * *", []string{"2026-11-04 10:18", "2026-11-04 10:19"}},
		{"*/15 * * * *", []string{"2026-11-04 10:30", "2026-11-04 10:45", "2026-11-04 11:00"}},
		{"0 3 * * *", []string{"2026-11-05 03:00", "2026-11-06 03:00"}},
		{"@hourly", []string{"2026-11-04 11:00"}},
		{"30 9 * * mon-fri", []string{"2026-11-05 09:30", "2026-11-06 09:30", "2026-11-09 09:30"}},
		{"0 0 1 jan,jul *", []string{"2027-01-01 00:00", "2027-07-01 00:00"}},
		{"0 12 29 2 *", []string{"2028-02-29 12:00", "2032-02-29 12:00"}},
		{"0 0 13 * 5", []string{"2026-11-06 00:00", "2026-11-13 00:00", "2026-11-20 00:00"}}, // the 13th or Fridays
		{"0 0 * * 7", []string{"2026-11-08 00:00"}},                                          // 7 is Sunday
		{"5-10/5 1 * * *", []string{"2026-11-05 01:05", "2026-11-05 01:10", "2026-11-06 01:05"}},
	} {
		s, err := Parse(tc.expr)
		if err != nil {
			t.Fatalf("%s: %v", tc.expr, err)
		}
		at := from
		for _, w := range tc.want {
			at = s.Next(at)
			if got := at.Format("2006-01-02 15:04"); got != w {
				t.Errorf("%s: got %s, want %s", tc.expr, got, w)
				break
			}
		}
	}
}

// TestDaylightSaving: a time skipped by the clock moving forward is
// skipped; one repeated by it moving back is due once.
func TestDaylightSaving(t *testing.T) {
	oslo := mustLoc(t, "Europe/Oslo")
	s, _ := Parse("30 2 * * *")
	// 2027-03-28: 02:00 becomes 03:00.
	if got := s.Next(time.Date(2027, 3, 27, 12, 0, 0, 0, oslo)); got.Day() != 29 || got.Hour() != 2 {
		t.Errorf("spring forward: %s", got)
	}
	// 2026-10-25: 03:00 becomes 02:00 again.
	first := s.Next(time.Date(2026, 10, 24, 12, 0, 0, 0, oslo))
	if first.Day() != 25 || first.Hour() != 2 || first.Minute() != 30 {
		t.Fatalf("fall back: %s", first)
	}
	if next := s.Next(first); next.Day() != 26 {
		t.Errorf("the repeated 02:30 was due again: %s", next)
	}
	hourly, _ := Parse("0 * * * *")
	if a, b := hourly.Next(time.Date(2026, 10, 25, 1, 30, 0, 0, oslo)), hourly.Next(time.Date(2026, 10, 25, 2, 0, 0, 0, oslo)); a.Hour() != 2 || b.Sub(a) != 2*time.Hour {
		t.Errorf("hourly over the fall back: %s then %s", a, b)
	}
}

// TestParseErrors: what's wrong is said per field.
func TestParseErrors(t *testing.T) {
	for expr, want := range map[string]string{
		"* * * *":      "5 fields",
		"60 * * * *":   "minute: 60 is outside 0-59",
		"* * * foo *":  "month: \"foo\" is not a number",
		"*/0 * * * *":  "step",
		"5-1 * * * *":  "backwards",
		"0 0 31 2 *":   "never happens",
		"0 0 30 2 mon": "", // a weekday still matches
	} {
		_, err := Parse(expr)
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", expr, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: %v, want %q", expr, err, want)
		}
	}
}
