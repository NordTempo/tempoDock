package core

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// errorsFor ticks the error sink into the store until the list for f
// holds n issues, and returns it. Followers read lines on their own
// goroutines, so it waits for them, ending errors that wait for a line
// as the tick does after errorQuiet.
func (h *harness) errorsFor(f ErrorFilter, n int) apitypes.ErrorList {
	h.t.Helper()
	var list apitypes.ErrorList
	eventually(h.t, fmt.Sprintf("%d error issues", n), func() bool {
		h.c.errorsTick(h.ctx, time.Now().Add(errorQuiet))
		var err error
		list, err = h.c.ListErrors(h.ctx, testSystem, f)
		if err != nil {
			h.t.Fatal(err)
		}
		return len(list.Issues) == n
	})
	return list
}

// followedNow waits until a service release's container is followed.
func (h *harness) followedNow(ref ServiceRef, number int64) {
	h.t.Helper()
	id := h.containerID(ref, number)
	eventually(h.t, "the container to be followed", func() bool { return h.followed(id) })
}

// A running container's errors become issues of its service: a stack
// trace is one issue, the same error again counts on it, info lines
// (Caddy's JSON access log among them) are no errors, secret values are
// masked, and a new issue alerts.
func TestLogErrorsBecomeIssues(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.set(web.Env(), "web", map[string]string{"STRIPE_KEY": "sk_live_51abcdef"}, false)
	h.deploy(web, "app:1")
	h.followedNow(web, 1)

	h.rt.AddStderr(h.ctr(web, 1),
		`{"level":"info","ts":1730980800.5,"logger":"http.log.access","msg":"handled request","status":200}`,
		`{"level":"error","msg":"charge 4411 failed with sk_live_51abcdef"}`,
		"panic: runtime error: index out of range [3] with length 2",
		"",
		"goroutine 1 [running]:",
		"main.total(...)",
		"\t/app/cart.go:42 +0x1d",
		"listening on :8080",
		`{"level":"error","msg":"charge 9 failed with sk_live_51abcdef"}`,
	)
	list := h.errorsFor(ErrorFilter{}, 2)
	byTitle := map[string]apitypes.ErrorIssue{}
	for _, i := range list.Issues {
		byTitle[i.Title] = i
	}
	charge := byTitle["charge 9 failed with "+secretMask]
	panicked := byTitle["panic: runtime error: index out of range [3] with length 2"]
	if charge.Count != 2 || charge.Source != apitypes.ErrorFromLogs || charge.State != apitypes.ErrorNew || charge.FirstRelease != 1 || charge.Service != "web" {
		t.Fatalf("charge issue = %+v (issues %+v)", charge, list.Issues)
	}
	if panicked.Count != 1 || panicked.Culprit != "main.total (/app/cart.go:42)" {
		t.Fatalf("panic issue = %+v", panicked)
	}
	if got := charge.Trend[len(charge.Trend)-1]; len(charge.Trend) != errorTrendHours || got != 2 {
		t.Fatalf("trend = %v, want 2 in its last hour", charge.Trend)
	}
	if len(list.Facets) != 1 || list.Facets[0] != (apitypes.ErrorFacet{Project: "shop", Environment: "production", Service: "web", Issues: 2, New: 2, Events: 3}) {
		t.Fatalf("facets = %+v", list.Facets)
	}
	d, err := h.c.GetError(h.ctx, testSystem, panicked.ID)
	if err != nil || !strings.Contains(d.Sample, "goroutine 1 [running]:") || strings.Contains(d.Sample, "listening") {
		t.Fatalf("panic detail = %+v, %v", d, err)
	}
	if d, _ := h.c.GetError(h.ctx, testSystem, charge.ID); strings.Contains(d.Sample, "sk_live") {
		t.Fatalf("a secret in the sample: %q", d.Sample)
	}

	key := "errors:" + itoa(h.serviceID(web)) + ":new:" + itoa(charge.ID)
	a := h.openAlert(key, true)
	if a.Kind != string(apitypes.AlertNewError) || !strings.Contains(a.Target, `"error_issue":`+itoa(charge.ID)) || a.ProjectID == nil || a.ServiceID == nil {
		t.Fatalf("alert = %+v", a)
	}
	if rel, err := h.c.GetRelease(h.ctx, testSystem, web, 1); err != nil || rel.NewErrors != 2 {
		t.Fatalf("release 1 new errors = %d, %v; want 2", rel.NewErrors, err)
	}
	// A day on, the alert resolves itself.
	h.c.expireErrorAlerts(h.ctx, time.Now().Add(errorNewFor+time.Minute))
	h.openAlert(key, false)
}

// sinkError puts one log error of a service into the sink, as a
// follower would.
func (h *harness) sinkError(ref ServiceRef, title string, at time.Time) {
	h.c.issues.add(h.serviceID(ref), "fp-"+title, pendingError{source: apitypes.ErrorFromLogs, title: title, count: 1, first: at, last: at, release: 1})
}

// A resolved issue leaves the open list; seen again, it regresses and
// alerts. Seven quiet days resolve an issue by itself, and a resolved
// one goes after 30 more.
func TestErrorResolveAndRegress(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	h.sinkError(web, "db down", time.Now())
	issue := h.errorsFor(ErrorFilter{}, 1).Issues[0]

	dev := h.in(h.member("dev@x", map[string]apitypes.Scope{"test": apitypes.ScopeDeploy}), "test")
	got, err := h.c.ResolveError(h.ctx, dev, issue.ID)
	if err != nil || got.State != apitypes.ErrorResolved || got.ResolvedBy != dev.Name || got.ResolvedAt == nil {
		t.Fatalf("resolved = %+v, %v", got, err)
	}
	h.openAlert("errors:"+itoa(h.serviceID(web))+":new:"+itoa(issue.ID), false)
	if open, _ := h.c.ListErrors(h.ctx, testSystem, ErrorFilter{}); len(open.Issues) != 0 || len(open.Facets) != 0 {
		t.Fatalf("open after resolving = %+v", open)
	}
	if res, _ := h.c.ListErrors(h.ctx, testSystem, ErrorFilter{State: "resolved"}); len(res.Issues) != 1 {
		t.Fatalf("resolved list = %+v", res)
	}
	events, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "error.resolve", Limit: 5})
	if err != nil || len(events) != 1 || events[0].Actor != dev.Name {
		t.Fatalf("audit = %+v, %v", events, err)
	}

	h.sinkError(web, "db down", time.Now())
	back := h.errorsFor(ErrorFilter{}, 1).Issues[0]
	if back.ID != issue.ID || back.State != apitypes.ErrorRegressed || back.Count != 2 || back.ResolvedAt != nil {
		t.Fatalf("regressed = %+v", back)
	}
	if a := h.openAlert("errors:"+itoa(h.serviceID(web))+":regressed:"+itoa(issue.ID), true); a.Kind != string(apitypes.AlertErrorRegressed) {
		t.Fatalf("regression alert = %+v", a)
	}

	if _, _, err := h.c.retainErrors(h.ctx, time.Now().Add(errorQuietFor+time.Hour)); err != nil {
		t.Fatal(err)
	}
	quiet, err := h.c.GetError(h.ctx, testSystem, issue.ID)
	if err != nil || quiet.State != apitypes.ErrorResolved || quiet.ResolvedBy != "" {
		t.Fatalf("after 7 quiet days = %+v, %v", quiet.ErrorIssue, err)
	}
	if _, _, err := h.c.retainErrors(h.ctx, time.Now().Add(errorKeepFor+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.GetError(h.ctx, testSystem, issue.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("after 30 more days: %v, want not found", err)
	}
}

// An issue is new in the release it first appeared in, or for a day;
// after both it is ongoing.
func TestErrorState(t *testing.T) {
	at := time.Now()
	for _, tc := range []struct {
		first, active int64
		age           time.Duration
		want          apitypes.ErrorState
	}{
		{first: 3, active: 3, age: 48 * time.Hour, want: apitypes.ErrorNew},
		{first: 3, active: 4, age: time.Hour, want: apitypes.ErrorNew},
		{first: 3, active: 4, age: 25 * time.Hour, want: apitypes.ErrorOngoing},
		{first: 0, active: 0, age: 25 * time.Hour, want: apitypes.ErrorOngoing},
	} {
		r := issueRow{FirstRelease: tc.first, ActiveRelease: tc.active, FirstSeen: at.Add(-tc.age)}
		if got := errorState(r, at); got != tc.want {
			t.Errorf("first %d, active %d, %s old: %s, want %s", tc.first, tc.active, tc.age, got, tc.want)
		}
	}
}

// Another org's members don't see or resolve an org's issues; reading
// takes read and resolving deploy; a token limited to another
// environment sees none.
func TestErrorPermissions(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	h.sinkError(web, "boom", time.Now())
	issue := h.errorsFor(ErrorFilter{}, 1).Issues[0]

	stranger := h.in(h.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin}), "acme")
	if list, err := h.c.ListErrors(h.ctx, stranger, ErrorFilter{}); err != nil || len(list.Issues) != 0 || len(list.Facets) != 0 {
		t.Fatalf("another org's list = %+v, %v", list, err)
	}
	if _, err := h.c.GetError(h.ctx, stranger, issue.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("another org's admin read the issue: %v", err)
	}
	if _, err := h.c.ResolveError(h.ctx, stranger, issue.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("another org's admin resolved the issue: %v", err)
	}

	reader := h.in(h.member("ro@x", map[string]apitypes.Scope{"test": apitypes.ScopeRead}), "test")
	if list, err := h.c.ListErrors(h.ctx, reader, ErrorFilter{Project: "shop", Environment: "production", Service: "web"}); err != nil || len(list.Issues) != 1 {
		t.Fatalf("a reader's list = %+v, %v", list, err)
	}
	if _, err := h.c.GetError(h.ctx, reader, issue.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.ResolveError(h.ctx, reader, issue.ID); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a reader resolved the issue: %v", err)
	}

	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	staging := testSystem
	staging.Kind, staging.Scope = "token", apitypes.ScopeDeploy
	es, err := h.c.envFor(h.ctx, testSystem, EnvRef{"shop", "staging"}, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	staging.ProjectID, staging.EnvironmentID, staging.ProjectName, staging.EnvironmentName = es.project.ID, es.env.ID, "shop", "staging"
	if list, err := h.c.ListErrors(h.ctx, staging, ErrorFilter{}); err != nil || len(list.Issues) != 0 || len(list.Facets) != 0 {
		t.Fatalf("a staging token's list = %+v, %v", list, err)
	}
	if _, err := h.c.GetError(h.ctx, staging, issue.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("a staging token read production's issue: %v", err)
	}
}

// 5xx spikes are traffic issues; a few 5xx among many answers aren't.
// Browser errors become frontend issues under the store's group, only
// from the server the service runs on, without a second new-error alert.
func TestTrafficAndFrontendErrors(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	api := h.service("api", apitypes.CreateServiceRequest{})
	rel := h.deploy(web, "app:1")
	h.deploy(api, "app:1")
	at := time.Now().Truncate(time.Minute).Unix()
	h.c.trafficErrors(map[int64]*serviceTraffic{
		h.serviceID(web): {counts: node.HostTraffic{Requests: 100, Status: [4]uint64{90, 0, 0, 10}}},
		h.serviceID(api): {counts: node.HostTraffic{Requests: 1000, Status: [4]uint64{990, 0, 0, 10}}},
	}, at)
	traffic := h.errorsFor(ErrorFilter{Project: "shop", Environment: "production", Service: "web"}, 1).Issues[0]
	if traffic.Source != apitypes.ErrorFromTraffic || traffic.Title != "5xx answers" || traffic.Count != 10 || traffic.FirstRelease != 1 {
		t.Fatalf("traffic issue = %+v", traffic)
	}
	if list, _ := h.c.ListErrors(h.ctx, testSystem, ErrorFilter{Project: "shop", Environment: "production", Service: "api"}); len(list.Issues) != 0 {
		t.Fatalf("api's 1%% 5xx made issues: %+v", list.Issues)
	}

	line := func(group uint64) string {
		return `{"t":` + strconv.FormatInt(time.Now().UnixMilli(), 10) + `,"s":1,"sv":` + itoa(h.serviceID(web)) + `,"rl":` + itoa(rel.ID) +
			`,"h":"web.apps.test","k":4,"n":"x is undefined","p":"/cart","v":1,"er":{"ty":"TypeError","st":"TypeError: x is undefined\n    at total (/assets/cart.js:1:2)","fr":"total (/assets/cart.js)","g":` +
			strconv.FormatUint(group, 10) + `}}`
	}
	batch := line(255) + "\n" + `{"t":1,"s":1,"sv":1,"k":1,"p":"/"}` + "\n" + line(255) + "\n"
	h.c.frontendErrors(h.c.localID, []byte(batch))
	h.c.frontendErrors(h.c.localID+99, []byte(line(4096)))
	list := h.errorsFor(ErrorFilter{Project: "shop", Environment: "production", Service: "web"}, 2)
	var fe apitypes.ErrorIssue
	for _, i := range list.Issues {
		if i.Source == apitypes.ErrorFromFrontend {
			fe = i
		}
	}
	if fe.Fingerprint != "fe:00000000000000ff" || fe.Count != 2 || fe.Title != "TypeError: x is undefined" || fe.Culprit != "total (/assets/cart.js)" || fe.FirstRelease != 1 {
		t.Fatalf("frontend issue = %+v (all %+v)", fe, list.Issues)
	}
	h.c.alerts.mu.Lock()
	defer h.c.alerts.mu.Unlock()
	if _, ok := h.c.alerts.open["errors:"+itoa(h.serviceID(web))+":new:"+itoa(fe.ID)]; ok {
		t.Fatal("a frontend issue alerted besides analytics' own new-error alert")
	}
}

// A flood of distinct errors is bounded: the sink keeps 50 fingerprints
// per service, a tick opens 10 issues per service, and a service has at
// most three error alerts open.
func TestErrorFloodIsBounded(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	for i := range 80 {
		h.sinkError(web, "error "+strconv.Itoa(i), time.Now())
	}
	h.c.issues.mu.Lock()
	pending := len(h.c.issues.pending)
	h.c.issues.mu.Unlock()
	if pending != maxPendingPerService {
		t.Fatalf("pending = %d, want %d", pending, maxPendingPerService)
	}
	list := h.errorsFor(ErrorFilter{}, maxNewIssues)
	if list.Facets[0].Issues != maxNewIssues {
		t.Fatalf("facets = %+v", list.Facets)
	}
	prefix := "errors:" + itoa(h.serviceID(web)) + ":"
	h.c.alerts.mu.Lock()
	defer h.c.alerts.mu.Unlock()
	open := 0
	for k := range h.c.alerts.open {
		if strings.HasPrefix(k, prefix) {
			open++
		}
	}
	if open != errorAlertsPerService {
		t.Fatalf("%d error alerts open, want %d", open, errorAlertsPerService)
	}
}
