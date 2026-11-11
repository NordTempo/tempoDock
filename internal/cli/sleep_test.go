package cli

import "testing"

// A sleep setting is minutes, a duration in whole minutes, or off.
func TestParseSleepAfter(t *testing.T) {
	for in, want := range map[string]int{"5": 5, "30m": 30, "2h": 120, "1440": 1440, "OFF": -1} {
		if got, err := parseSleepAfter(in); err != nil || got != want {
			t.Errorf("%q: %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"4", "90s", "25h", "-5", "soon"} {
		if _, err := parseSleepAfter(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
