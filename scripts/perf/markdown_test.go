package main

import (
	"errors"
	"strings"
	"testing"
)

// A failed benchmark run keeps its failure and drops the passing output
// around it, so the PR comment stays under GitHub's size limit.
func TestMDErrorKeepsFailures(t *testing.T) {
	out := strings.Join([]string{
		"bench: go test -bench: exit status 1",
		"goos: linux",
		"pkg: github.com/NordTempo/tempoDock/internal/core",
		strings.Repeat("BenchmarkOverview-8   100   1000 ns/op\n", 2000),
		"PASS",
		"ok  \tgithub.com/NordTempo/tempoDock/internal/core\t1.0s",
		"--- FAIL: BenchmarkArchivedLogsTail",
		"    logarchive_bench_test.go:15: the disk is nearly full",
		"FAIL\tgithub.com/NordTempo/tempoDock/internal/node/logarchive\t0.014s",
	}, "\n")
	got := mdError(errors.New(out))
	for _, want := range []string{"exit status 1", "--- FAIL: BenchmarkArchivedLogsTail", "the disk is nearly full", "FAIL\tgithub.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("mdError dropped %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ns/op") || strings.Contains(got, "goos:") {
		t.Errorf("mdError kept passing output:\n%s", got)
	}

	if long := mdError(errors.New(strings.Repeat("x", 3*mdErrorMax))); len(long) > mdErrorMax+100 {
		t.Errorf("mdError is %d bytes, want about %d", len(long), mdErrorMax)
	}
}
