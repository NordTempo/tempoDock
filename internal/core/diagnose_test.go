package core

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Each rule on the output and facts of a real failure of its kind; the
// cases that must not match keep the rules from guessing.
func TestDiagnoseRules(t *testing.T) {
	web := releaseConfig{Port: 8080, Published: true, HealthCheckPath: "/health", HealthCheckTimeout: 60, MemoryMB: 512,
		Env: map[string]string{"PORT": "8080", "NODE_ENV": "production"}}
	health := func(msg string, lines ...string) diagInput {
		return diagInput{service: "web", environment: "production", step: apitypes.ReleaseHealthChecking, code: apitypes.CodeHealthCheckFailed,
			err: msg, cfg: web, health: node.ParseHealthFailure(msg), container: lines}
	}
	const closed = "health check timed out after 1m0s: connect to 127.0.0.1:20001: connection closed, is the app listening on the container port?"
	pre := func(code int, lines ...string) diagInput {
		return diagInput{service: "web", environment: "production", step: apitypes.ReleaseStarting, code: apitypes.CodeDeployFailed,
			err: "pre-deploy command exited", cfg: web, pre: &apitypes.DiagnosisPreDeploy{Command: "npm run migrate", ExitCode: &code, Tail: diagLines(lines)}}
	}
	withLast := func(in diagInput, env map[string]string) diagInput {
		in.last = &releaseConfig{Env: env}
		return in
	}
	for _, tc := range []struct {
		name    string
		in      diagInput
		rule    string
		summary string
	}{
		{"port mismatch, fastify", health(closed, `{"level":30,"msg":"Server listening at http://0.0.0.0:3000"}`),
			"port_mismatch", "The app listens on :3000, Dock checks :8080"},
		{"port mismatch, express", health(closed, "Example app listening on port 3000"),
			"port_mismatch", "The app listens on :3000, Dock checks :8080"},
		{"loopback only, uvicorn", health(closed, "INFO:     Uvicorn running on http://127.0.0.1:8080 (Press CTRL+C to quit)"),
			"loopback_only", "The app listens only on 127.0.0.1 inside its container; make it listen on 0.0.0.0:8080"},
		{"nothing listening, no port said", health(closed, "starting worker"),
			"not_listening", "Nothing answered on :8080 within 60s"},
		{"health path 404", health("health check timed out after 1m0s: GET http://127.0.0.1:20001/health returned 404 Not Found", "listening on :8080"),
			"health_path", "GET /health answered 404: the app has no such path"},
		{"oom", health("health check failed: " + node.ExitedError(137, true, 512<<20).Error()),
			"oom_killed", "Killed for using more than its 512 MB memory limit"},
		{"killed 137 without an oom event", health("health check failed: " + node.ExitedError(137, false, 512<<20).Error()),
			"oom_killed", "Killed (exit code 137), most likely for using more than its 512 MB memory limit"},
		{"exit 127", health("health check failed: "+node.ExitedError(127, false, 0).Error(), "/bin/sh: 1: bun: not found"),
			"missing_binary", "The start command isn't in the image"},
		{"missing variable removed since", withLast(health("health check failed: "+node.ExitedError(1, false, 0).Error(),
			"Error: DATABASE_URL is not set"), map[string]string{"DATABASE_URL": "postgres://x"}),
			"missing_variable", "DATABASE_URL isn't set (removed since the last good release)"},
		{"python KeyError", health("health check failed: "+node.ExitedError(1, false, 0).Error(), "KeyError: 'SECRET_KEY_BASE'"),
			"missing_variable", "SECRET_KEY_BASE isn't set"},
		{"a variable that is set isn't missing", health("health check failed: "+node.ExitedError(1, false, 0).Error(), "NODE_ENV is required to be production", "Error: boom"),
			"exited", "The app exited with code 1 while starting"},
		{"pre-deploy failed", pre(1, "Running migrations", "error: relation \"users\" already exists"),
			"pre_deploy_failed", "The pre-deploy command exited with code 1; what it changed before is not rolled back"},
		{"wrong architecture", health("health check failed: "+node.ExitedError(1, false, 0).Error(), "exec /app/server: exec format error"),
			"wrong_arch", "The image was built for another CPU architecture than the server's"},
		{"pull refused", diagInput{step: apitypes.ReleaseBuilding, code: apitypes.CodeBuildFailed, pulled: true, cfg: web,
			err: "pull ghcr.io/x/y:1: unauthorized: authentication required"},
			"pull_failed", "The image's registry refused access; add or fix its registry credentials"},
		{"anonymous pull refused", diagInput{step: apitypes.ReleaseBuilding, code: apitypes.CodeBuildFailed, pulled: true, cfg: web,
			err: `pull ghcr.io/x/y:1: Head "https://ghcr.io/v2/x/y/manifests/1": no basic auth credentials`},
			"pull_failed", "The image's registry refused access; add or fix its registry credentials"},
		{"push timed out", diagInput{step: apitypes.ReleaseBuilding, code: apitypes.CodeBuildFailed, cfg: web,
			err: `build failed: push 127.0.0.1:5000/dock/p1_e1_s1:5: failed to do request: Head "https://127.0.0.1:5000/v2/dock/p1_e1_s1/blobs/sha256:e3a8": net/http: timeout awaiting response headers`},
			"push_failed", "The image built, but the platform registry didn't answer in time; deploy again, and if it keeps failing an operator checks `dock registry` and the registry's storage"},
		{"a build step failed", diagInput{step: apitypes.ReleaseBuilding, code: apitypes.CodeBuildFailed, cfg: web,
			err: "build failed: failed to solve: process \"/bin/sh -c pnpm build\" did not complete successfully: exit code: 1"},
			"build_failed", "The build failed"},
		{"offline", diagInput{step: apitypes.ReleaseBuilding, code: apitypes.CodeDeployFailed, offline: true, cfg: web},
			"server_offline", "The server went offline during building; the previous release kept serving"},
		{"no rule", diagInput{step: apitypes.ReleaseHealthChecking, code: apitypes.CodeDeployFailed, cfg: web, err: "routes could not be updated: boom"},
			"", "No rule matched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := diagnose(&tc.in)
			if got.cause.Rule != tc.rule || got.cause.Summary != tc.summary {
				t.Errorf("cause = %q %q, want %q %q", got.cause.Rule, got.cause.Summary, tc.rule, tc.summary)
			}
			matched := 0
			for _, c := range got.checked {
				if c.Matched {
					matched++
				}
				if c.Why == "" {
					t.Errorf("check %s has no reason", c.Rule)
				}
			}
			if want := min(len(tc.rule), 1) + len(got.also); matched != want {
				t.Errorf("%d checks matched, want %d: %+v", matched, want, got.checked)
			}
		})
	}
}

// A failed release keeps its diagnosis: the cause, the line it matched,
// what changed, and secret values masked for a reader but not an admin.
func TestReleaseDiagnosis(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "shop:1", Port: 8080, HealthCheckPath: "/health"})
	h.deploy(web, "shop:1")

	if _, err := h.c.SetVariables(h.ctx, testSystem, EnvRef{"shop", "production"},
		apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"SENTRY_DSN": "https://sentry-secret-key@o1"}}); err != nil {
		t.Fatal(err)
	}
	h.rt.FailHealthCheck(func(node.ContainerSpec) error {
		return errors.New("health check timed out after 1m0s: connect to 127.0.0.1:20002: connection closed, is the app listening on the container port?")
	})
	release := h.rt.HoldHealthChecks()
	rel, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "shop:2"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, rel.Number, apitypes.ReleaseHealthChecking)
	h.rt.AddLogs(h.ctr(web, rel.Number), "sentry ready, dsn https://sentry-secret-key@o1", `{"msg":"Server listening at http://0.0.0.0:3000"}`)
	release()
	if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseFailed {
		t.Fatalf("release = %s, want failed", r.Status)
	}

	got, err := h.c.GetRelease(h.ctx, testSystem, web, rel.Number)
	if err != nil {
		t.Fatal(err)
	}
	if got.Diagnosis == nil || got.Diagnosis.Rule != "port_mismatch" || got.Diagnosis.Summary != "The app listens on :3000, Dock checks :8080" {
		t.Fatalf("release diagnosis = %+v", got.Diagnosis)
	}
	list, err := h.c.ListReleases(h.ctx, testSystem, web, 10)
	if err != nil || list[0].Diagnosis == nil || list[0].Diagnosis.ID != got.Diagnosis.ID {
		t.Fatalf("listed releases: %v, %+v", err, list[0].Diagnosis)
	}

	admin, err := h.c.GetDiagnosis(h.ctx, testSystem, web, got.Diagnosis.ID)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Release != rel.Number || admin.LastGoodRelease != 1 || admin.Health == nil || admin.Health.Result != "not_listening" {
		t.Errorf("diagnosis = release %d, last good %d, health %+v", admin.Release, admin.LastGoodRelease, admin.Health)
	}
	if admin.Changes == nil || admin.Changes.Image == nil || !slices.Equal(admin.Changes.Variables.Added, []string{"SENTRY_DSN"}) {
		t.Errorf("changes = %+v", admin.Changes)
	}
	if len(admin.ContainerTail) != 2 || !admin.ContainerTail[1].Matched || admin.ContainerTail[0].Matched {
		t.Errorf("container tail = %+v", admin.ContainerTail)
	}
	if !strings.Contains(admin.ContainerTail[0].Text, "sentry-secret-key") {
		t.Errorf("an admin reads %q, want the value as written", admin.ContainerTail[0].Text)
	}
	if !slices.ContainsFunc(admin.Checked, func(c apitypes.DiagnosisCheck) bool { return c.Rule == "oom_killed" && !c.Matched }) {
		t.Errorf("checked = %+v, want oom_killed ruled out", admin.Checked)
	}

	reader := Actor{Kind: "token", OrgID: 1, Name: "ro", TokenID: 1, Scope: apitypes.ScopeRead}
	masked, err := h.c.GetDiagnosis(h.ctx, reader, web, got.Diagnosis.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(masked.ContainerTail[0].Text, "sentry-secret-key") || !strings.Contains(masked.ContainerTail[0].Text, secretMask) {
		t.Errorf("a reader reads %q, want the value masked", masked.ContainerTail[0].Text)
	}
	// The failed deploy's alert says the cause and points at it.
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	if a := h.openAlert("deploy:"+alertServiceKey(ss.svc.ID, "", "", ""), true); !strings.Contains(a.Message, "Likely cause: The app listens on :3000, Dock checks :8080.") ||
		!strings.HasPrefix(a.Command, "dock diagnose -r 2") {
		t.Errorf("deploy alert = %q, %q", a.Message, a.Command)
	}
	if _, err := h.c.GetDiagnosis(h.ctx, reader, web, got.Diagnosis.ID+1); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("another ID: %v, want not found", err)
	}
}

// A crash loop is diagnosed before its alert opens, so the alert says the
// likely cause and points at the live release's diagnosis.
func TestCrashLoopDiagnosis(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "shop:1", Port: 8080})
	// The release's live event, which resets the engine's restart count,
	// is published just after it reads active: wait for it, so the
	// restarts below come after it on the bus, as they would.
	sub := h.c.bus.subscribe(func(e busEvent) bool {
		return e.ev.Type == apitypes.EventRelease && e.ev.Release != nil && e.ev.Release.Status == apitypes.ReleaseActive
	})
	h.deploy(web, "shop:1")
	select {
	case <-sub.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no live event")
	}
	sub.close()
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	h.rt.AddStderr(h.ctr(web, 1), "Error: DATABASE_URL is not set")
	// Docker restarts it again and again, as its events say: the first
	// one the alert engine sees is history, the next three a loop.
	restart := func() {
		h.c.changeStatus(ss, func(s *apitypes.ServiceStatus) { s.State, s.ExitCode = apitypes.ServiceRunning, 1; s.RestartCount++ })
	}
	for range 4 {
		restart()
	}
	key := "crash:" + alertServiceKey(ss.svc.ID, "", "", "")
	a := h.openAlert(key, true)
	if !strings.Contains(a.Message, "Likely cause: DATABASE_URL isn't set.") || !strings.HasPrefix(a.Command, "dock diagnose") ||
		!strings.Contains(a.Target, `"release":1`) {
		t.Fatalf("alert = %+v", a)
	}
	rel, err := h.c.GetRelease(h.ctx, testSystem, web, 1)
	if err != nil || rel.Diagnosis == nil || rel.Diagnosis.Kind != apitypes.DiagnosisCrashLoop {
		t.Fatalf("active release's diagnosis: %v %+v", err, rel.Diagnosis)
	}
	d, err := h.c.GetDiagnosis(h.ctx, testSystem, web, rel.Diagnosis.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Cause.Rule != "missing_variable" || d.Failure.Exit == nil || d.Failure.Exit.ExitCode == nil || *d.Failure.Exit.ExitCode != 1 ||
		d.Headline() != "Release 1 of shop/production/web crash looped while live" {
		t.Errorf("diagnosis = %+v", d)
	}

	// The project overview carries it with the active release.
	ov, err := h.c.Overview(h.ctx, testSystem, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if ar := ov.Environments[0].Services[0].ActiveRelease; ar == nil || ar.Diagnosis == nil || ar.Diagnosis.ID != rel.Diagnosis.ID {
		t.Errorf("overview's active release = %+v", ar)
	}

	// Later restarts keep the cause; the next crash loop is diagnosed anew.
	restart()
	// Handled to the end, so resolving below doesn't race it: a restart
	// that found the alert resolved would diagnose a new crash loop.
	if !h.c.alertsHandled(h.ctx) {
		t.Fatal("the alert engine stopped")
	}
	if a := h.openAlert(key, true); !strings.Contains(a.Message, "Likely cause") {
		t.Errorf("after another restart: %q", a.Message)
	}
	h.c.resolveAlert(h.ctx, key, time.Now())
	h.c.alerts.mu.Lock()
	_, kept := h.c.alerts.causes[key]
	h.c.alerts.mu.Unlock()
	if kept {
		t.Error("a resolved alert kept its cause")
	}
}

// An unhealthy service is diagnosed once a restart didn't help, and its
// alert says why.
func TestUnhealthyDiagnosis(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000, HealthCheckPath: "/healthz"})
	h.deploy(web, "nginx:1")
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	id, name, key := h.ctrID(web, 1), h.ctr(web, 1), healthAlertKey(ss.svc.ID)
	h.rt.SetHealth(id, node.HealthHealthy, "")
	h.until("healthy", func() bool { return h.healthOf(ss.svc.ID).Health == apitypes.HealthHealthy })

	h.rt.SetHealth(id, node.HealthUnhealthy, "GET /healthz returned 404")
	h.until("first restart", func() bool { return h.restarts(name) == 1 })
	if a := h.openAlert(key, true); strings.Contains(a.Message, "Likely cause") {
		t.Fatalf("diagnosed before a restart failed to help: %q", a.Message)
	}
	h.rt.SetHealth(id, node.HealthUnhealthy, "GET /healthz returned 404")
	h.until("critical", func() bool { return h.openAlert(key, true).Severity == string(apitypes.SeverityCritical) })
	if a := h.openAlert(key, true); !strings.Contains(a.Message, "Likely cause: GET /healthz answered 404: the app has no such path.") {
		t.Fatalf("alert = %q", a.Message)
	}
	list, err := h.c.ListDiagnoses(h.ctx, testSystem, web, 0, 5)
	if err != nil || len(list) != 1 || list[0].Kind != apitypes.DiagnosisUnhealthy || list[0].Release != 1 {
		t.Fatalf("diagnoses = %v %+v", err, list)
	}
}
