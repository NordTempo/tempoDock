package main

import "testing"

// A re-recorded baseline shows its limit's move: a raise warns, a cut is
// info, an unchanged one prints nothing.
func TestBudgetChanges(t *testing.T) {
	o := options{checks: []string{"binary", "tempobase-binary", "bench"}}
	old := baseline{BinaryBytes: 60 << 20, TempobaseBinaryBytes: 19 << 20, Benchmarks: map[string]bench{"a": {AllocsOp: 4}, "b": {AllocsOp: 4}}}
	cur := baseline{BinaryBytes: 66 << 20, TempobaseBinaryBytes: 18 << 20, Benchmarks: map[string]bench{"a": {AllocsOp: 5}, "b": {AllocsOp: 3}}}
	rows := budgetChanges(o, old, cur)
	want := []struct {
		check string
		r     result
	}{{"binary size budget", warn}, {"tempobase binary size budget", info}, {"bench allocs baselines raised", warn}}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for i, w := range want {
		if rows[i].check != w.check || rows[i].result != w.r {
			t.Errorf("row %d = %+v, want %s %s", i, rows[i], w.check, w.r)
		}
	}
	if rows := budgetChanges(o, cur, cur); len(rows) != 0 {
		t.Errorf("unchanged baseline: %+v", rows)
	}
}
