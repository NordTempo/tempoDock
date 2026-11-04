package core

import (
	"archive/tar"
	"bytes"
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// jobRuns returns a job's runs, newest first.
func (h *harness) jobRuns(ref ServiceRef, name string) []apitypes.CommandRun {
	h.t.Helper()
	runs, err := h.c.JobRuns(h.ctx, testSystem, ref, name)
	if err != nil {
		h.t.Fatal(err)
	}
	return runs
}

// setJob sets a job as the system.
func (h *harness) setJob(ref ServiceRef, name string, req apitypes.SetJobRequest) apitypes.Job {
	h.t.Helper()
	j, err := h.c.SetJob(h.ctx, testSystem, ref, name, req)
	if err != nil {
		h.t.Fatal(err)
	}
	return j
}

// settle waits in a synctest bubble until ok holds, or for a while when
// ok is nil (to see that nothing more happens). synctest.Wait alone can
// return early: SQLite's busy wait sleeps, which counts as blocked, while
// a write that will land is still waiting for its lock. Each step moves
// the fake clock a millisecond, nothing a schedule notices.
func settle(ok func() bool) {
	for range 2000 {
		synctest.Wait()
		if ok != nil && ok() {
			return
		}
		if ok == nil {
			for range 50 {
				time.Sleep(time.Millisecond)
				synctest.Wait()
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// statuses lists runs' statuses, newest first.
func statuses(runs []apitypes.CommandRun) []apitypes.RunStatus {
	out := make([]apitypes.RunStatus, len(runs))
	for i, r := range runs {
		out[i] = r.Status
	}
	return out
}

// TestJobSchedule: a job runs at each due time on the one timer, as the
// system, with the job's command in the active image; the scheduler sleeps
// in between and a disabled job doesn't run.
func TestJobSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
		h.deploy(web, "app:1")
		start := time.Now()
		j := h.setJob(web, "tick", apitypes.SetJobRequest{Schedule: "*/10 * * * *", Command: "bin/tick"})
		if j.NextAt == nil || j.NextAt.Sub(start) > 10*time.Minute || j.Timezone != "UTC" || j.Overlap != apitypes.OverlapSkip {
			t.Fatalf("job = %+v", j)
		}
		time.Sleep(25 * time.Minute)
		settle(func() bool { return len(h.jobRuns(web, "tick")) == 2 })
		runs := h.jobRuns(web, "tick")
		if len(runs) != 2 || runs[0].Trigger != apitypes.TriggerSchedule || runs[0].Status != apitypes.RunSucceeded || runs[0].CreatedBy != "system" {
			t.Fatalf("runs after 25 min = %+v", runs)
		}
		if job := h.lastJob(); job.Script != "bin/tick" || len(job.Mounts) != 0 {
			t.Fatalf("job spec = %+v", job)
		}
		if _, err := h.c.SetJobEnabled(h.ctx, testSystem, web, "tick", false); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
		settle(nil)
		if n := len(h.jobRuns(web, "tick")); n != 2 {
			t.Fatalf("a disabled job ran: %d runs", n)
		}
		// One-off commands keep their own history.
		if list, _ := h.c.ListRuns(h.ctx, testSystem, web); len(list) != 0 {
			t.Fatalf("job runs in the command list: %+v", list)
		}
	})
}

// scheduling reports whether the job scheduler's goroutine runs.
func (h *harness) scheduling() bool {
	h.c.jobs.mu.Lock()
	defer h.c.jobs.mu.Unlock()
	return h.c.jobs.scheduling
}

// TestJobSchedulerIdle: without an enabled job there is no scheduler
// goroutine or timer; setting one starts it, disabling the last one ends
// it again.
func TestJobSchedulerIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
		settle(func() bool { return !h.scheduling() })
		if h.scheduling() {
			t.Fatal("the scheduler runs without jobs")
		}
		h.setJob(web, "tick", apitypes.SetJobRequest{Schedule: "@daily", Command: "x"})
		settle(nil)
		if !h.scheduling() {
			t.Fatal("the scheduler didn't start for a job")
		}
		if _, err := h.c.SetJobEnabled(h.ctx, testSystem, web, "tick", false); err != nil {
			t.Fatal(err)
		}
		settle(func() bool { return !h.scheduling() })
		if h.scheduling() {
			t.Fatal("the scheduler kept running with no enabled job")
		}
	})
}

// TestJobOverlap: a job still running when it comes due again is
// skipped, and recorded so; with overlap allow both run.
func TestJobOverlap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
		h.deploy(web, "app:1")
		release := make(chan struct{})
		h.rt.OnJobContext(func(ctx context.Context, _ node.JobSpec) (node.JobResult, error) {
			select {
			case <-release:
				return node.JobResult{}, nil
			case <-ctx.Done():
				return node.JobResult{}, ctx.Err()
			}
		})
		h.setJob(web, "slow", apitypes.SetJobRequest{Schedule: "* * * * *", Command: "slow"})
		time.Sleep(150 * time.Second)
		settle(func() bool { return len(h.jobRuns(web, "slow")) == 2 })
		got := statuses(h.jobRuns(web, "slow"))
		if len(got) != 2 || got[0] != apitypes.RunSkipped || got[1] != apitypes.RunRunning {
			t.Fatalf("skip: %v", got)
		}
		if _, err := h.c.RunJobNow(h.ctx, testSystem, web, "slow"); CodeOf(err) != apitypes.CodeConflict {
			t.Fatalf("run now while running: %v", err)
		}
		h.setJob(web, "slow", apitypes.SetJobRequest{Schedule: "* * * * *", Command: "slow", Overlap: apitypes.OverlapAllow})
		time.Sleep(time.Minute)
		settle(func() bool { return len(h.jobRuns(web, "slow")) >= 3 })
		if got := statuses(h.jobRuns(web, "slow")); got[0] != apitypes.RunRunning {
			t.Fatalf("allow: %v", got)
		}
		close(release)
		synctest.Wait()
	})
}

// TestJobRecovery: after downtime a job runs once for its latest missed
// time, not once per missed time, and isn't repeated after a restart.
func TestJobRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
		h.deploy(web, "app:1")
		h.setJob(web, "hourly", apitypes.SetJobRequest{Schedule: "@hourly", Command: "x"})
		h.c.Close()
		time.Sleep(5 * time.Hour)
		h.c = h.start()
		settle(func() bool { return len(h.jobRuns(web, "hourly")) >= 1 })
		if n := len(h.jobRuns(web, "hourly")); n != 1 {
			t.Fatalf("after 5 hours down: %d runs, want 1", n)
		}
		h.restart()
		settle(nil)
		if n := len(h.jobRuns(web, "hourly")); n != 1 {
			t.Fatalf("a restart ran it again: %d runs", n)
		}
	})
}

// TestJobPermissions: jobs are set, removed and run with deploy, read
// with read; a locked environment refuses tokens but keeps running the
// schedule.
func TestJobPermissions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
		h.deploy(web, "app:1")
		reader := Actor{Kind: "token", OrgID: 1, Name: "viewer", TokenID: 1, Scope: apitypes.ScopeRead}
		deployer := Actor{Kind: "token", OrgID: 1, Name: "ci", TokenID: 2, Scope: apitypes.ScopeDeploy}
		req := apitypes.SetJobRequest{Schedule: "0 * * * *", Command: "x"}
		if _, err := h.c.SetJob(h.ctx, reader, web, "a", req); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("read sets: %v", err)
		}
		if _, err := h.c.SetJob(h.ctx, deployer, web, "a", req); err != nil {
			t.Fatal(err)
		}
		if jobs, err := h.c.ListJobs(h.ctx, reader, web); err != nil || len(jobs) != 1 {
			t.Fatalf("read lists: %+v %v", jobs, err)
		}
		for _, bad := range []apitypes.SetJobRequest{
			{Schedule: "every day", Command: "x"},
			{Schedule: "@daily", Command: "x", Timezone: "Mars/Olympus"},
			{Schedule: "@daily", Command: " "},
		} {
			if _, err := h.c.SetJob(h.ctx, deployer, web, "b", bad); CodeOf(err) != apitypes.CodeValidation {
				t.Errorf("%+v: %v", bad, err)
			}
		}
		h.lock("production")
		if _, err := h.c.SetJob(h.ctx, deployer, web, "a", req); CodeOf(err) != apitypes.CodeEnvironmentLocked {
			t.Fatalf("set in a locked environment: %v", err)
		}
		if _, err := h.c.RunJobNow(h.ctx, deployer, web, "a"); CodeOf(err) != apitypes.CodeEnvironmentLocked {
			t.Fatalf("run now in a locked environment: %v", err)
		}
		time.Sleep(time.Hour)
		settle(func() bool { r := h.jobRuns(web, "a"); return len(r) == 1 && r[0].Status != apitypes.RunRunning })
		if runs := h.jobRuns(web, "a"); len(runs) != 1 || runs[0].Status != apitypes.RunSucceeded {
			t.Fatalf("the schedule in a locked environment: %+v", runs)
		}
	})
}

// TestJobAlerts: a failing run raises job_failed, the next good one
// resolves it; a service without an active release skips.
func TestJobAlerts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
		h.setJob(web, "nightly", apitypes.SetJobRequest{Schedule: "@hourly", Command: "x"})
		time.Sleep(time.Hour)
		settle(func() bool { return len(h.jobRuns(web, "nightly")) == 1 })
		if runs := h.jobRuns(web, "nightly"); len(runs) != 1 || runs[0].Status != apitypes.RunSkipped || runs[0].Error == "" {
			t.Fatalf("without a release: %+v", runs)
		}
		h.deploy(web, "app:1")
		h.rt.OnJob(func(node.JobSpec) (node.JobResult, error) { return node.JobResult{ExitCode: 1, Output: "boom"}, nil })
		run, err := h.c.RunJobNow(h.ctx, testSystem, web, "nightly")
		if err != nil || run.Trigger != apitypes.TriggerManual {
			t.Fatalf("run now: %+v %v", run, err)
		}
		settle(func() bool { open, _ := h.c.ListAlerts(h.ctx, testSystem, false, 0, 0); return len(open) == 1 })
		open, _ := h.c.ListAlerts(h.ctx, testSystem, false, 0, 0)
		if len(open) != 1 || open[0].Kind != apitypes.AlertJobFailed {
			t.Fatalf("alerts after a failure = %+v", open)
		}
		h.rt.OnJob(nil)
		time.Sleep(time.Hour)
		settle(func() bool { open, _ := h.c.ListAlerts(h.ctx, testSystem, false, 0, 0); return len(open) == 0 })
		if open, _ := h.c.ListAlerts(h.ctx, testSystem, false, 0, 0); len(open) != 0 {
			t.Fatalf("alerts after a success = %+v", open)
		}
		// A stopped service skips its schedule; running it now still works.
		if _, err := h.c.StopService(h.ctx, testSystem, web); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
		settle(func() bool { r := h.jobRuns(web, "nightly"); return len(r) > 0 && r[0].Status == apitypes.RunSkipped })
		if r := h.jobRuns(web, "nightly"); r[0].Status != apitypes.RunSkipped || !strings.Contains(r[0].Error, "stopped") {
			t.Fatalf("stopped: %+v", r[0])
		}
		if _, err := h.c.RunJobNow(h.ctx, testSystem, web, "nightly"); err != nil {
			t.Fatalf("run now on a stopped service: %v", err)
		}
		settle(nil)
		if err := h.c.RemoveJob(h.ctx, testSystem, web, "nightly"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.c.JobRuns(h.ctx, testSystem, web, "nightly"); CodeOf(err) != apitypes.CodeNotFound {
			t.Fatalf("history of a removed job: %v", err)
		}
	})
}

// TestRepoJobs: a source deploy's dock.toml makes its jobs the service's
// file jobs, which the API can't change or remove; the next file without
// them removes them, and UI jobs are left alone.
func TestRepoJobs(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	deploy := func(toml string) {
		t.Helper()
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: "dock.toml", Mode: 0o644, Size: int64(len(toml)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(toml))
		_ = tw.Close()
		rel, err := h.c.DeploySource(h.ctx, testSystem, web, &buf, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseActive {
			t.Fatalf("release %d: %s %s", r.Number, r.Status, r.Error)
		}
	}
	h.setJob(web, "mine", apitypes.SetJobRequest{Schedule: "@daily", Command: "ui"})
	deploy("[[jobs]]\nname = \"cleanup\"\nschedule = \"0 3 * * *\"\ntimezone = \"Europe/Oslo\"\ncommand = \"bin/cleanup\"\n")
	var jobs []apitypes.Job
	var err error
	eventually(t, "the file job", func() bool {
		jobs, err = h.c.ListJobs(h.ctx, testSystem, web)
		return len(jobs) == 2
	})
	if err != nil || len(jobs) != 2 || jobs[0].Name != "cleanup" || jobs[0].Source != apitypes.JobFromRepo || jobs[0].Timezone != "Europe/Oslo" {
		t.Fatalf("jobs = %+v %v", jobs, err)
	}
	if _, err := h.c.SetJob(h.ctx, testSystem, web, "cleanup", apitypes.SetJobRequest{Schedule: "@daily", Command: "x"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("set a file job: %v", err)
	}
	if err := h.c.RemoveJob(h.ctx, testSystem, web, "cleanup"); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("remove a file job: %v", err)
	}
	if _, err := h.c.SetJobEnabled(h.ctx, testSystem, web, "cleanup", false); err != nil {
		t.Fatalf("disable a file job: %v", err)
	}
	deploy("[deploy]\nport = 8080\n")
	// The sync follows the release going active.
	eventually(t, "the file job to go", func() bool {
		jobs, _ = h.c.ListJobs(h.ctx, testSystem, web)
		return len(jobs) == 1 && jobs[0].Name == "mine"
	})
}
