package mcp_test

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// TestJobTools: set_job creates a job with its next time, run_job waits
// for the run and returns its output, job_history and list_jobs show it,
// remove_job removes it.
func TestJobTools(t *testing.T) {
	f := newFixture(t)
	s := f.connect("shop-agent", nil)
	if dep := call(t, s, "deploy_image", map[string]any{"service": "web", "image": "app:2"}); dep.isError {
		t.Fatalf("deploy_image = %q", dep.summary)
	}
	var job apitypes.Job
	set := call(t, s, "set_job", map[string]any{"service": "web", "job": "nightly", "schedule": "0 3 * * *", "timezone": "Europe/Oslo", "command": "bin/cleanup"})
	set.decode(t, &job)
	if set.isError || job.NextAt == nil || job.Timezone != "Europe/Oslo" {
		t.Fatalf("set_job = %q %+v", set.summary, job)
	}
	if bad := call(t, s, "set_job", map[string]any{"service": "web", "job": "x", "schedule": "every night", "command": "y"}); !bad.isError {
		t.Fatal("a bad schedule was taken")
	}
	f.rt.OnJob(func(node.JobSpec) (node.JobResult, error) { return node.JobResult{Output: "cleaned\n"}, nil })
	var run apitypes.CommandRun
	res := call(t, s, "run_job", map[string]any{"service": "web", "job": "nightly"})
	res.decode(t, &run)
	if res.isError || run.Status != apitypes.RunSucceeded || run.Output != "cleaned\n" || run.Job != "nightly" || run.Trigger != apitypes.TriggerManual {
		t.Fatalf("run_job = %q %+v", res.summary, run)
	}
	var runs []apitypes.CommandRun
	call(t, s, "job_history", map[string]any{"service": "web", "job": "nightly"}).decode(t, &runs)
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("job_history = %+v", runs)
	}
	var jobs []apitypes.Job
	call(t, s, "list_jobs", map[string]any{"service": "web"}).decode(t, &jobs)
	if len(jobs) != 1 || jobs[0].LastRun == nil || jobs[0].LastRun.ID != run.ID {
		t.Fatalf("list_jobs = %+v", jobs)
	}
	if rm := call(t, s, "remove_job", map[string]any{"service": "web", "job": "nightly"}); rm.isError {
		t.Fatalf("remove_job = %q", rm.summary)
	}
}
