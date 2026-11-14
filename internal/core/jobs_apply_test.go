package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// jobsFile is a project file of shop/production with service web and the
// given TOML for its jobs.
func jobsFile(jobs string) string {
	return "version = 1\n[[environments]]\nname = \"production\"\n[[environments.services]]\nname = \"web\"\nimage = \"app:1\"\n" + jobs
}

// A file's jobs are set at once, changed when the file changes them, kept
// when left out and removed with --prune; a dock.toml job is never
// touched, and a file job of its name is refused.
func TestApplyJobs(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.setJob(web, "old", apitypes.SetJobRequest{Schedule: "@hourly", Command: "bin/old"})
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.applyRepoJobs(h.ctx, ss, []repoJob{{Name: "repo", Schedule: "@daily", Command: "bin/repo"}}); err != nil {
		t.Fatal(err)
	}
	apply := func(file string, prune bool) apitypes.ApplyPlan {
		t.Helper()
		res, err := h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file, Prune: prune})
		if err != nil {
			t.Fatal(err)
		}
		return res.Plan
	}
	jobs := func() map[string]apitypes.Job {
		t.Helper()
		js, err := h.c.ListJobs(h.ctx, testSystem, web)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]apitypes.Job{}
		for _, j := range js {
			out[j.Name] = j
		}
		return out
	}

	file := jobsFile("[[environments.services.jobs]]\nname = \"cleanup\"\nschedule = \"0 3 * * *\"\ntimezone = \"Europe/Oslo\"\ncommand = \"bin/cleanup\"\n")
	plan, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file})
	if err != nil {
		t.Fatal(err)
	}
	if got := lines(plan.Changes); !slices.Equal(got, []string{"production web job add cleanup"}) {
		t.Fatalf("plan changes = %v", got)
	}
	if got := lines(plan.Kept); !slices.Equal(got, []string{"production web job remove old"}) {
		t.Fatalf("plan kept = %v", got)
	}
	if len(jobs()) != 2 {
		t.Fatal("a plan set a job")
	}
	apply(file, false)
	js := jobs()
	if c := js["cleanup"]; c.Source != apitypes.JobFromAPI || c.Timezone != "Europe/Oslo" || c.TimeoutSeconds != apitypes.DefaultRunTimeoutSeconds || !c.Enabled {
		t.Fatalf("cleanup = %+v", c)
	}
	if _, ok := js["old"]; !ok || js["repo"].Source != apitypes.JobFromRepo {
		t.Fatalf("jobs = %v", js)
	}
	// Applied again: nothing to change.
	if plan := apply(file, false); len(plan.Changes) != 0 {
		t.Fatalf("second apply = %v", lines(plan.Changes))
	}

	// Changed and pruned: old goes, repo stays.
	file = jobsFile("[[environments.services.jobs]]\nname = \"cleanup\"\nschedule = \"@daily\"\ncommand = \"bin/cleanup\"\noverlap = \"allow\"\nenabled = false\n")
	plan = apply(file, true)
	if got := lines(plan.Changes); !slices.Equal(got, []string{"production web job update cleanup", "production web job remove old"}) {
		t.Fatalf("pruned plan = %v", got)
	}
	js = jobs()
	if c := js["cleanup"]; len(js) != 2 || c.Schedule != "@daily" || c.Timezone != "UTC" || c.Overlap != apitypes.OverlapAllow || c.Enabled {
		t.Fatalf("jobs after prune = %+v", js)
	}

	// The export writes the file's job back, not dock.toml's, and
	// applying the export changes nothing.
	ex, exported := h.exportTOML("shop")
	if got := ex.Environments[0].Services[0].Jobs; len(got) != 1 || got[0].Name != "cleanup" || got[0].Enabled == nil || *got[0].Enabled {
		t.Fatalf("exported jobs = %+v", got)
	}
	if plan := apply(exported, true); len(plan.Changes) != 0 {
		t.Fatalf("applying the export = %v", lines(plan.Changes))
	}

	// A file job named like a dock.toml one.
	_, err = h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: jobsFile("[[environments.services.jobs]]\nname = \"repo\"\nschedule = \"@daily\"\ncommand = \"x\"\n")})
	if CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "dock.toml") {
		t.Fatalf("file job named like a dock.toml job: %v", err)
	}
	// A bad schedule is refused before anything changes.
	_, err = h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: jobsFile("[[environments.services.jobs]]\nname = \"x\"\nschedule = \"every day\"\ncommand = \"x\"\n")})
	if CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "job x of service web") {
		t.Fatalf("bad schedule: %v", err)
	}
}

// A new service's jobs wait for it to exist: a manual line when the file
// is only staged, set with --deploy once the deploy created the service.
func TestApplyJobsOfNewServices(t *testing.T) {
	h := newHarness(t)
	h.service("db", apitypes.CreateServiceRequest{Image: "db:1"})
	file := jobsFile("[[environments.services.jobs]]\nname = \"cleanup\"\nschedule = \"@daily\"\ncommand = \"bin/cleanup\"\n")
	plan, err := h.c.PlanProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file})
	if err != nil {
		t.Fatal(err)
	}
	if got := lines(plan.Manual); !slices.Equal(got, []string{"production web job add cleanup"}) {
		t.Fatalf("plan manual = %v", got)
	}
	res, err := h.c.ApplyProject(h.ctx, testSystem, "shop", apitypes.ApplyRequest{File: file, Deploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(lines(res.Plan.Changes), "production web job add cleanup") {
		t.Fatalf("deploy plan = %v", lines(res.Plan.Changes))
	}
	js, err := h.c.ListJobs(h.ctx, testSystem, ServiceRef{"shop", "production", "web"})
	if err != nil || len(js) != 1 || js[0].Name != "cleanup" {
		t.Fatalf("jobs = %+v %v", js, err)
	}
}

// A locked environment refuses a token's file that changes jobs, quoting
// the job, and sets nothing.
func TestApplyJobsLocked(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.LockEnvironment(h.ctx, testSystem, web.Env(), apitypes.LockEnvironmentRequest{Reason: "live"}); err != nil {
		t.Fatal(err)
	}
	token := h.token(testSystem, apitypes.CreateTokenRequest{Name: "claude", Scope: apitypes.ScopeDeploy})
	file := jobsFile("[[environments.services.jobs]]\nname = \"cleanup\"\nschedule = \"@daily\"\ncommand = \"bin/cleanup\"\n")
	_, err := h.c.ApplyProject(h.ctx, token, "shop", apitypes.ApplyRequest{File: file})
	if CodeOf(err) != apitypes.CodeEnvironmentLocked || !strings.Contains(err.Error(), "+ web job cleanup") {
		t.Fatalf("apply in a locked environment: %v", err)
	}
	if js, _ := h.c.ListJobs(h.ctx, testSystem, web); len(js) != 0 {
		t.Fatalf("set a job despite the lock: %+v", js)
	}
}
