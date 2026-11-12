package main

import "testing"

// The idle reading is the median of the daemons' readings, shown with
// all of them in start order; a single reading shows alone.
func TestMedianOf(t *testing.T) {
	m, s := medianOf([]int64{30 << 20, 28 << 20, 32 << 20})
	if m != 30<<20 || s != "30.0 MB anon (median of 30.0, 28.0, 32.0)" {
		t.Errorf("three: %d %q", m, s)
	}
	if m, s := medianOf([]int64{29 << 20}); m != 29<<20 || s != "29.0 MB anon" {
		t.Errorf("one: %d %q", m, s)
	}
}
