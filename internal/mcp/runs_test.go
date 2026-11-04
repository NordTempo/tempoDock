package mcp_test

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// TestRunCommand: run_command waits for the end and returns the exit code
// and the output's tail; list_runs shows the run; a timeout past the
// tool's limit is refused.
func TestRunCommand(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)
	if dep := call(t, s, "deploy_image", map[string]any{"service": "web", "image": "app:2"}); dep.isError {
		t.Fatalf("deploy_image = %q", dep.summary)
	}
	f.rt.OnJob(func(spec node.JobSpec) (node.JobResult, error) {
		return node.JobResult{ExitCode: 2, Output: "seeded 3 rows\n"}, nil
	})
	res := call(t, s, "run_command", map[string]any{"service": "web", "command": "bin/seed"})
	var run apitypes.CommandRun
	res.decode(t, &run)
	if res.isError || run.Status != apitypes.RunFailed || run.ExitCode == nil || *run.ExitCode != 2 || run.Output != "seeded 3 rows\n" || !strings.Contains(res.summary, "code 2") {
		t.Fatalf("run_command = %q %+v", res.summary, run)
	}
	var runs []apitypes.CommandRun
	list := call(t, s, "list_runs", map[string]any{"service": "web"})
	list.decode(t, &runs)
	if len(runs) != 1 || runs[0].Command != "bin/seed" {
		t.Fatalf("list_runs = %q %+v", list.summary, runs)
	}
	if bad := call(t, s, "run_command", map[string]any{"service": "web", "command": "x", "timeout_seconds": 5000}); !bad.isError {
		t.Fatal("a timeout over 900 s was taken")
	}
}
