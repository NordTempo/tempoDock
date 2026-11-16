package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// prComments is the fake GitHub's pull request comments.
type prComments struct {
	mu      sync.Mutex
	bodies  map[int64]string // by comment ID
	created int
	edited  int
}

// withComments makes the fake GitHub keep acme/shop's pull request
// comments.
func (g *fakeGitHub) withComments() *prComments {
	pc := &prComments{bodies: map[int64]string{}}
	g.mux.HandleFunc("GET /repos/acme/shop/issues/{n}/comments", func(w http.ResponseWriter, r *http.Request) {
		pc.mu.Lock()
		defer pc.mu.Unlock()
		list := []githubComment{}
		for id, b := range pc.bodies {
			list = append(list, githubComment{ID: id, Body: b})
		}
		_ = json.NewEncoder(w).Encode(list)
	})
	g.mux.HandleFunc("POST /repos/acme/shop/issues/{n}/comments", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		pc.mu.Lock()
		pc.created++
		id := int64(100 + pc.created)
		pc.bodies[id] = in.Body
		pc.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(githubComment{ID: id, Body: in.Body})
	})
	g.mux.HandleFunc("PATCH /repos/acme/shop/issues/comments/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		var in struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		pc.mu.Lock()
		defer pc.mu.Unlock()
		if _, ok := pc.bodies[id]; !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		pc.edited++
		pc.bodies[id] = in.Body
		_ = json.NewEncoder(w).Encode(githubComment{ID: id, Body: in.Body})
	})
	return pc
}

// only returns the one comment, failing on none or more.
func (pc *prComments) only(t *testing.T) string {
	t.Helper()
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if len(pc.bodies) != 1 || pc.created != 1 {
		t.Fatalf("%d comments (%d created), want 1", len(pc.bodies), pc.created)
	}
	for _, b := range pc.bodies {
		return b
	}
	return ""
}

// previewHarness is an App harness with project shop's staging: web
// built from acme/shop@main in apps/web with a domain, api running an
// image, a Postgres database, a listed and an unlisted literal, a
// reference to api and one to an org variable; previews
// are on from staging, copying web.API_URL.
func previewHarness(t *testing.T) (*harness, *fakeGitHub, *prComments, string) {
	h, gh, appHook := appHarness(t)
	pc := gh.withComments()
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	staging := EnvRef{"shop", "staging"}
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	for _, req := range []apitypes.CreateServiceRequest{{Name: "web", Port: 3000}, {Name: "api", Image: "api:1"}, {Name: "db", Template: apitypes.TemplatePostgres}} {
		if _, err := h.c.CreateService(h.ctx, testSystem, staging, req); err != nil {
			t.Fatal(err)
		}
	}
	api, db := ServiceRef{"shop", "staging", "api"}, ServiceRef{"shop", "staging", "db"}
	h.wait(db, 1)
	h.deploy(api, "api:1")
	web := ServiceRef{"shop", "staging", "web"}
	if _, err := h.c.GitConnect(h.ctx, testSystem, web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/web"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.GenerateDomain(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	h.set(staging, "", map[string]string{"SECRET": "s3cret"}, false)
	if _, err := h.c.SetOrgVariable(h.ctx, testSystem, "PROD_DB", "postgres://prod"); err != nil {
		t.Fatal(err)
	}
	h.set(staging, "web", map[string]string{"API_URL": "https://api.example.com", "TOKEN": "tok_live", "DB": "${{org.PROD_DB}}", "API": "${{api.PORT}}"}, true)
	h.servicePreviews(web, apitypes.SetServicePreviewsRequest{Build: true, CopyVariables: []string{"API_URL"}})
	if _, err := h.c.SetEnvironmentPreviews(h.ctx, testSystem, staging, apitypes.SetEnvironmentPreviewsRequest{MaxPreviews: 2}); err != nil {
		t.Fatal(err)
	}
	return h, gh, pc, appHook
}

// servicePreviews sets what a service does in previews, as the system.
func (h *harness) servicePreviews(ref ServiceRef, req apitypes.SetServicePreviewsRequest) {
	h.t.Helper()
	if _, err := h.c.SetServicePreviews(h.ctx, testSystem, ref, req); err != nil {
		h.t.Fatal(err)
	}
}

// prEvent is a pull_request event of acme/shop (repository 42).
type prEvent struct {
	action, sha, base string
	number            int64
	updated           time.Time
	fork, merged      bool
}

// deliver sends ev to the App's webhook as delivery id.
func (h *harness) deliver(appHook, id string, ev prEvent) (string, error) {
	h.t.Helper()
	body := prBody(ev)
	return h.c.GitWebhook(h.ctx, GitDelivery{HookID: appHook, Event: "pull_request", ID: id, Signature: sign("app-secret", body), Body: body})
}

// prBody is ev as GitHub's pull_request payload.
func prBody(ev prEvent) []byte {
	head := map[string]any{"id": fakeRepoID, "full_name": "acme/shop"}
	if ev.fork {
		head = map[string]any{"id": 77, "full_name": "mallory/shop"}
	}
	base := ev.base
	if base == "" {
		base = "main"
	}
	body, _ := json.Marshal(map[string]any{
		"action": ev.action, "number": ev.number,
		"pull_request": map[string]any{
			"html_url": fmt.Sprintf("https://github.com/acme/shop/pull/%d", ev.number), "title": "Faster checkout",
			"updated_at": ev.updated, "merged": ev.merged, "user": map[string]string{"login": "ada"},
			"head": map[string]any{"ref": "faster-checkout", "sha": ev.sha, "repo": head},
			"base": map[string]any{"ref": base, "repo": map[string]any{"id": fakeRepoID, "full_name": "acme/shop"}},
		},
		"repository":   map[string]any{"id": fakeRepoID, "full_name": "acme/shop"},
		"installation": map[string]any{"id": fakeInstall},
		"sender":       map[string]string{"login": "ada"},
	})
	return body
}

// A pull request gets a preview cloned from staging: its web builds the
// head commit, api runs staging's image, web gets an address, and only
// the listed literal is copied. One comment says where it is, edited as
// it changes; replays and stale events change nothing; closing archives
// it and frees its volumes and hostname.
func TestPreviewLifecycle(t *testing.T) {
	h, gh, pc, appHook := previewHarness(t)
	t0 := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	msg, err := h.deliver(appHook, "d1", prEvent{action: "opened", number: 7, sha: shaA, updated: t0})
	if err != nil {
		t.Fatal(err)
	}
	if msg != "shop: pull request #7: preview pr-7 created for aaaaaaa" {
		t.Fatalf("answer %q", msg)
	}
	web, api := ServiceRef{"shop", "pr-7", "web"}, ServiceRef{"shop", "pr-7", "api"}
	rels := h.waitReleases(web, 1)
	if r := h.wait(web, rels[0].Number); r.Status != apitypes.ReleaseActive || r.Commit == nil || r.Commit.SHA != shaA {
		t.Fatalf("web release %+v", r)
	}
	if r := h.wait(api, h.waitReleases(api, 1)[0].Number); r.Status != apitypes.ReleaseActive || r.Source != apitypes.SourcePromote {
		t.Fatalf("api release %+v", r)
	}
	env, err := h.c.GetEnvironment(h.ctx, testSystem, EnvRef{"shop", "pr-7"})
	if err != nil || env.Kind != apitypes.KindNonProduction || env.Preview == nil || env.Preview.Number != 7 || env.Preview.URL != "https://github.com/acme/shop/pull/7" {
		t.Fatalf("environment %+v, %v", env, err)
	}
	if got := h.revealed(web); got["API_URL"] != "https://api.example.com" || got["TOKEN"] != "" || got["DB"] != "" || got["API"] != "${{api.PORT}}" {
		t.Fatalf("web's variables %v: only the listed literal and references within the preview are copied", got)
	}
	shared, _ := h.c.ListVariables(h.ctx, testSystem, EnvRef{"shop", "pr-7"}, "", true)
	if slices.ContainsFunc(shared, func(v apitypes.Variable) bool { return v.Key == "SECRET" }) {
		t.Fatal("an unlisted shared secret reached the preview")
	}
	if db := h.revealed(ServiceRef{"shop", "pr-7", "db"}); db["POSTGRES_PASSWORD"] == "" || db["POSTGRES_PASSWORD"] == h.revealed(ServiceRef{"shop", "staging", "db"})["POSTGRES_PASSWORD"] {
		t.Fatal("the preview's database password isn't new")
	}
	var pv apitypes.Preview
	waitFor(t, func() bool {
		pv, err = h.c.GetPreview(h.ctx, testSystem, EnvRef{"shop", "pr-7"})
		return err == nil && pv.Status == apitypes.PreviewLive
	})
	host := h.generatedHost(ServiceRef{"shop", "pr-7", "web"})
	if len(pv.URLs) != 1 || pv.URLs[0] != (apitypes.PreviewURL{Service: "web", URL: "https://" + host}) || !generatedLike("web-shop-pr-7", "apps.test").MatchString(host) {
		t.Fatalf("preview %+v", pv)
	}
	// A project file leaves it out: pull requests make previews.
	if ex, err := h.c.ExportProject(h.ctx, testSystem, "shop"); err != nil || slices.ContainsFunc(ex.Environments, func(e apitypes.EnvironmentExport) bool { return strings.HasPrefix(e.Name, "pr-") }) {
		t.Fatalf("export: %+v, %v", ex.Environments, err)
	}
	waitFor(t, func() bool { return strings.Contains(pc.only(t), "**live**") })
	if body := pc.only(t); !strings.Contains(body, "https://"+host) || !strings.Contains(body, "sleeps after 30 min without traffic") {
		t.Fatalf("comment %q", body)
	}
	waitFor(t, func() bool {
		st, _, _ := gh.seen()
		return slices.Contains(st, "aaaaaaa success dock/preview https://"+host)
	})

	// A redelivery, and a late event older than the last one applied,
	// change nothing.
	if msg, err := h.deliver(appHook, "d1", prEvent{action: "synchronize", number: 7, sha: shaB, updated: t0.Add(time.Minute)}); err != nil || !strings.Contains(msg, "already received") {
		t.Fatalf("redelivery: %q, %v", msg, err)
	}
	if msg, err := h.deliver(appHook, "d0", prEvent{action: "closed", number: 7, sha: shaA, updated: t0.Add(-time.Minute)}); err != nil || !strings.Contains(msg, "older than the last event") {
		t.Fatalf("stale close: %q, %v", msg, err)
	}

	// A new commit redeploys web; the comment is edited, not added.
	if msg, err := h.deliver(appHook, "d2", prEvent{action: "synchronize", number: 7, sha: shaB, updated: t0.Add(time.Minute)}); err != nil || !strings.Contains(msg, "updated to bbbbbbb") {
		t.Fatalf("synchronize: %q, %v", msg, err)
	}
	rels = h.waitReleases(web, 2)
	if r := h.wait(web, rels[0].Number); r.Commit == nil || r.Commit.SHA != shaB {
		t.Fatalf("web release %+v", r)
	}
	waitFor(t, func() bool { return strings.Contains(pc.only(t), "`bbbbbbb`") })

	// Closing archives it: hidden, routes and hostname gone, volumes removed.
	if msg, err := h.deliver(appHook, "d3", prEvent{action: "closed", number: 7, sha: shaB, merged: true, updated: t0.Add(2 * time.Minute)}); err != nil || !strings.Contains(msg, "preview pr-7 archived") {
		t.Fatalf("close: %q, %v", msg, err)
	}
	envs, _ := h.c.ListEnvironments(h.ctx, testSystem, "shop")
	if slices.ContainsFunc(envs, func(e apitypes.Environment) bool { return strings.HasPrefix(e.Name, "pr-") }) {
		t.Fatalf("closed preview listed: %+v", envs)
	}
	if exists, _ := h.store.HostnameExists(h.ctx, host); exists {
		t.Fatal("the preview's hostname is still taken")
	}
	var left int
	if err := h.store.DB.QueryRowContext(h.ctx, "SELECT count(*) FROM variables JOIN environments ON environments.id = variables.environment_id WHERE environments.name LIKE 'pr-7~%'").Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d variables left in the closed preview, %v", left, err)
	}
	waitFor(t, func() bool {
		return slices.ContainsFunc(h.rt.Ops(), func(op string) bool { return strings.HasPrefix(op, "remove volume dock_") })
	})
	waitFor(t, func() bool { return strings.Contains(pc.only(t), "**closed**") })
	if p, err := h.c.GetPreviews(h.ctx, testSystem, "shop"); err != nil || len(p.Previews) != 0 {
		t.Fatalf("open previews after close: %+v, %v", p, err)
	}

	// Reopened, it gets a new preview under the same name.
	if msg, err := h.deliver(appHook, "d4", prEvent{action: "reopened", number: 7, sha: shaB, updated: t0.Add(3 * time.Minute)}); err != nil || !strings.Contains(msg, "preview pr-7 created") {
		t.Fatalf("reopen: %q, %v", msg, err)
	}
	h.waitReleases(ServiceRef{"shop", "pr-7", "web"}, 1)
}

// Pull requests from forks, into another branch, or beyond the cap get
// no preview; pushes to the head branch never deploy one.
func TestPreviewRefusals(t *testing.T) {
	h, gh, _, appHook := previewHarness(t)
	t0 := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	if msg, _ := h.deliver(appHook, "f1", prEvent{action: "opened", number: 1, sha: shaA, updated: t0, fork: true}); !strings.Contains(msg, "comes from a fork") {
		t.Fatalf("fork: %q", msg)
	}
	waitFor(t, func() bool {
		st, _, _ := gh.seen()
		return slices.ContainsFunc(st, func(s string) bool { return strings.HasPrefix(s, "aaaaaaa error dock/preview") })
	})
	if msg, _ := h.deliver(appHook, "b1", prEvent{action: "opened", number: 2, sha: shaA, updated: t0, base: "develop"}); !strings.Contains(msg, "merges into develop and the services previewing acme/shop deploy main") {
		t.Fatalf("other branch: %q", msg)
	}
	for i, n := range []int64{3, 4} {
		if msg, err := h.deliver(appHook, fmt.Sprintf("o%d", i), prEvent{action: "opened", number: n, sha: shaA, updated: t0}); err != nil || !strings.Contains(msg, "created") {
			t.Fatalf("pull request %d: %q, %v", n, msg, err)
		}
	}
	if msg, _ := h.deliver(appHook, "o3", prEvent{action: "opened", number: 5, sha: shaA, updated: t0}); !strings.Contains(msg, "2 are open, the most shop/staging allows") {
		t.Fatalf("over the cap: %q", msg)
	}
	h.waitReleases(ServiceRef{"shop", "pr-3", "web"}, 1)

	// A push to the head branch reaches staging's connection only, which
	// deploys main: the preview's connection never gets pushes.
	var p map[string]any
	_ = json.Unmarshal(pushBody("refs/heads/faster-checkout", shaC, "apps/web/x"), &p)
	p["repository"] = map[string]any{"id": fakeRepoID, "full_name": "acme/shop"}
	p["installation"] = map[string]any{"id": fakeInstall}
	body, _ := json.Marshal(p)
	msg, err := h.c.GitWebhook(h.ctx, GitDelivery{HookID: appHook, Event: "push", ID: "p1", Signature: sign("app-secret", body), Body: body})
	if err != nil || strings.Contains(msg, "pr-3") || strings.Contains(msg, "queued") {
		t.Fatalf("push answer %q, %v", msg, err)
	}

	// Allowing forks lets one in, once a slot is free.
	if _, err := h.c.ClosePreview(h.ctx, testSystem, EnvRef{"shop", "pr-4"}); err != nil {
		t.Fatal(err)
	}
	h.servicePreviews(ServiceRef{"shop", "staging", "web"}, apitypes.SetServicePreviewsRequest{Build: true, AllowForks: true})
	if msg, _ := h.deliver(appHook, "f2", prEvent{action: "synchronize", number: 1, sha: shaB, updated: t0.Add(time.Minute), fork: true}); !strings.Contains(msg, "preview pr-1 created") {
		t.Fatalf("fork allowed: %q", msg)
	}
	if pv, err := h.c.GetPreview(h.ctx, testSystem, EnvRef{"shop", "pr-1"}); err != nil || !pv.Fork || pv.Branch != "pull/1/head" {
		t.Fatalf("fork preview %+v, %v", pv, err)
	}
	// Forks off again: the open fork preview gets no new commits.
	h.servicePreviews(ServiceRef{"shop", "staging", "web"}, apitypes.SetServicePreviewsRequest{Build: true})
	if msg, _ := h.deliver(appHook, "f3", prEvent{action: "synchronize", number: 1, sha: shaC, updated: t0.Add(2 * time.Minute), fork: true}); !strings.Contains(msg, "comes from a fork") {
		t.Fatalf("fork commit after forks were turned off: %q", msg)
	}
	if pv, err := h.c.GetPreview(h.ctx, testSystem, EnvRef{"shop", "pr-1"}); err != nil || pv.Commit == shaC {
		t.Fatalf("fork preview took the new commit: %+v, %v", pv, err)
	}

	// Turned off, new pull requests get nothing, and open previews still
	// close with their pull requests.
	h.servicePreviews(ServiceRef{"shop", "staging", "web"}, apitypes.SetServicePreviewsRequest{})
	if msg, _ := h.deliver(appHook, "x1", prEvent{action: "opened", number: 6, sha: shaA, updated: t0}); strings.Contains(msg, "created") {
		t.Fatalf("previews off: %q", msg)
	}
	waitFor(t, func() bool {
		pv, err := h.c.GetPreview(h.ctx, testSystem, EnvRef{"shop", "pr-3"})
		return err == nil && pv.Status == apitypes.PreviewLive
	})
	if msg, err := h.deliver(appHook, "x2", prEvent{action: "closed", number: 3, sha: shaA, updated: t0.Add(time.Hour)}); err != nil || !strings.Contains(msg, "preview pr-3 archived") {
		t.Fatalf("close with previews off: %q, %v", msg, err)
	}
}

// Only admins of the environment set previews up, never in a preview;
// deploy may close one, read may look.
func TestPreviewPermissions(t *testing.T) {
	h, _, _, appHook := previewHarness(t)
	if _, err := h.deliver(appHook, "d1", prEvent{action: "opened", number: 9, sha: shaA, updated: time.Now()}); err != nil {
		t.Fatal(err)
	}
	web, staging := ServiceRef{"shop", "staging", "web"}, EnvRef{"shop", "staging"}
	for _, a := range []Actor{
		{Kind: "token", OrgID: 1, Name: "deploy", Scope: apitypes.ScopeDeploy},
		{Kind: "token", OrgID: 2, Name: "other-org", Scope: apitypes.ScopeAdmin},
	} {
		if _, err := h.c.SetServicePreviews(h.ctx, a, web, apitypes.SetServicePreviewsRequest{}); CodeOf(err) != apitypes.CodeForbidden && CodeOf(err) != apitypes.CodeNotFound {
			t.Fatalf("%s set a service's previews: %v", a.Name, err)
		}
		if _, err := h.c.SetEnvironmentPreviews(h.ctx, a, staging, apitypes.SetEnvironmentPreviewsRequest{}); CodeOf(err) != apitypes.CodeForbidden && CodeOf(err) != apitypes.CodeNotFound {
			t.Fatalf("%s set an environment's previews: %v", a.Name, err)
		}
	}
	if _, err := h.c.SetServicePreviews(h.ctx, testSystem, ServiceRef{"shop", "pr-9", "web"}, apitypes.SetServicePreviewsRequest{Build: true}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("previews of a preview: %v", err)
	}
	if _, err := h.c.SetServicePreviews(h.ctx, testSystem, web, apitypes.SetServicePreviewsRequest{Build: true, CopyVariables: []string{"$(rm)"}}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a bad variable name: %v", err)
	}
	// Building pull requests needs a GitHub source.
	if _, err := h.c.SetServicePreviews(h.ctx, testSystem, ServiceRef{"shop", "staging", "api"}, apitypes.SetServicePreviewsRequest{Build: true}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("api built pull requests without a source: %v", err)
	}
	ro := Actor{Kind: "token", OrgID: 1, Name: "ro", Scope: apitypes.ScopeRead}
	if s, err := h.c.GetPreviews(h.ctx, ro, "shop"); err != nil || !s.Enabled || s.Base != "staging" || len(s.Previews) != 1 {
		t.Fatalf("read previews: %+v, %v", s, err)
	}
	if _, err := h.c.ClosePreview(h.ctx, ro, EnvRef{"shop", "pr-9"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read closed a preview: %v", err)
	}
	if _, err := h.c.ClosePreview(h.ctx, testSystem, EnvRef{"shop", "staging"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("closed staging: %v", err)
	}
}

// A webhook connection speaks for pull requests only from the base
// environment: anyone who may connect a service elsewhere gets its
// webhook secret, and a pull request it signs must neither make, update
// nor close a preview.
func TestPreviewWebhookOnlyFromBase(t *testing.T) {
	h, _ := gitHarness(t)
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"staging", "dev"} {
		if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: env}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.c.CreateService(h.ctx, testSystem, EnvRef{"shop", env}, apitypes.CreateServiceRequest{Name: "web", Port: 3000}); err != nil {
			t.Fatal(err)
		}
	}
	base := h.connect(ServiceRef{"shop", "staging", "web"}, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/web"})
	other := h.connect(ServiceRef{"shop", "dev", "web"}, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main", Dir: "apps/web"})
	h.servicePreviews(ServiceRef{"shop", "staging", "web"}, apitypes.SetServicePreviewsRequest{Build: true})
	send := func(k *hook, id string, ev prEvent) string {
		t.Helper()
		body := prBody(ev)
		msg, err := h.c.GitWebhook(h.ctx, GitDelivery{HookID: k.id, Event: "pull_request", ID: id, Signature: sign(k.secret, body), Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	t0 := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	preview := func() bool {
		_, err := h.c.GetPreview(h.ctx, testSystem, EnvRef{"shop", "pr-8"})
		return err == nil
	}
	if msg := send(other, "x1", prEvent{action: "opened", number: 8, sha: shaA, updated: t0}); !strings.Contains(msg, "doesn't build pull requests") || preview() {
		t.Fatalf("dev's webhook opened a preview: %q", msg)
	}
	if msg := send(base, "b1", prEvent{action: "opened", number: 8, sha: shaA, updated: t0}); !strings.Contains(msg, "preview pr-8 created") {
		t.Fatalf("staging's webhook: %q", msg)
	}
	h.waitReleases(ServiceRef{"shop", "pr-8", "web"}, 1)
	for i, action := range []string{"synchronize", "closed"} {
		if msg := send(other, fmt.Sprintf("x%d", i+2), prEvent{action: action, number: 8, sha: shaB, updated: t0.Add(time.Minute)}); !strings.Contains(msg, "doesn't build pull requests") {
			t.Fatalf("dev's webhook sent %s: %q", action, msg)
		}
	}
	if pv, err := h.c.GetPreview(h.ctx, testSystem, EnvRef{"shop", "pr-8"}); err != nil || pv.Commit != shaA || pv.Status == apitypes.PreviewClosed {
		t.Fatalf("preview after dev's events: %+v, %v", pv, err)
	}
	// Turned off, the base's webhook still closes its open preview.
	h.servicePreviews(ServiceRef{"shop", "staging", "web"}, apitypes.SetServicePreviewsRequest{})
	waitFor(t, func() bool {
		pv, err := h.c.GetPreview(h.ctx, testSystem, EnvRef{"shop", "pr-8"})
		return err == nil && pv.Status == apitypes.PreviewLive
	})
	if msg := send(base, "b2", prEvent{action: "closed", number: 8, sha: shaA, updated: t0.Add(2 * time.Minute)}); !strings.Contains(msg, "archived") || preview() {
		t.Fatalf("staging's close: %q", msg)
	}
	var conns int
	if err := h.store.DB.QueryRowContext(h.ctx, "SELECT count(*) FROM git_connections JOIN services ON services.id = git_connections.service_id JOIN environments ON environments.id = services.environment_id WHERE environments.name LIKE 'pr-8~%'").Scan(&conns); err != nil || conns != 0 {
		t.Fatalf("%d git connections left in the closed preview, %v", conns, err)
	}
}
