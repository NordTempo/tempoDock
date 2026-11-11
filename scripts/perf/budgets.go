package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// budgetChanges are rows for the budgets a baseline moves against an
// older one: the binary limits and benchmarks recorded with more
// allocations. A raise is a warning, so it shows in CI's perf comment and
// the pull request has to say why; a cut is info. Only the checks in
// o.checks are compared, as only they can be re-recorded.
func budgetChanges(o options, old, cur baseline) []row {
	var rows []row
	limit := func(bytes int64, abs int64) int64 {
		l := int64(float64(bytes) * (1 + binaryGrowthLimit))
		if abs > 0 {
			l = min(l, abs)
		}
		return l
	}
	change := func(name string, before, after int64) {
		moved := fmt.Sprintf("%s → %s", mb(before), mb(after))
		switch {
		case before == 0 || before == after:
		case after > before:
			rows = append(rows, row{name, moved, "raised: say why in the PR", warn})
		default:
			rows = append(rows, row{name, moved, "lowered", info})
		}
	}
	if slices.Contains(o.checks, "binary") && old.BinaryBytes > 0 {
		change("binary size budget", limit(old.BinaryBytes, 0), limit(cur.BinaryBytes, 0))
	}
	if slices.Contains(o.checks, "tempobase-binary") && old.TempobaseBinaryBytes > 0 {
		change("tempobase binary size budget", limit(old.TempobaseBinaryBytes, tempobaseBinaryBudget), limit(cur.TempobaseBinaryBytes, tempobaseBinaryBudget))
	}
	if slices.Contains(o.checks, "bench") {
		var raised []string
		for _, name := range slices.Sorted(maps.Keys(cur.Benchmarks)) {
			if b, ok := old.Benchmarks[name]; ok && cur.Benchmarks[name].AllocsOp > b.AllocsOp {
				raised = append(raised, fmt.Sprintf("%s %.0f → %.0f", name, b.AllocsOp, cur.Benchmarks[name].AllocsOp))
			}
		}
		if len(raised) > 0 {
			rows = append(rows, row{"bench allocs baselines raised", strings.Join(raised, "; "), "raised: say why in the PR", warn})
		}
	}
	return rows
}

// baselineAt reads the baseline file as committed at rev (git show), for
// budgetChanges. An unknown rev, as in a shallow clone without it, is an
// error the caller reports and moves on from.
func baselineAt(rev, path string) (baseline, error) {
	var b baseline
	out, err := exec.Command("git", "show", rev+":"+path).Output()
	if err != nil {
		return b, fmt.Errorf("git show %s:%s: %w", rev, path, err)
	}
	return b, json.Unmarshal(out, &b)
}

// addBudgetChanges adds budgetChanges against o.baseRev's baseline to t,
// or says on stderr why it can't.
func addBudgetChanges(o options, cur baseline, t *table) {
	if o.baseRev == "" {
		return
	}
	old, err := baselineAt(o.baseRev, o.baseline)
	if err != nil {
		fmt.Fprintf(os.Stderr, "perf: budgets not compared with %s: %v\n", o.baseRev, err)
		return
	}
	t.rows = append(t.rows, budgetChanges(o, old, cur)...)
}
