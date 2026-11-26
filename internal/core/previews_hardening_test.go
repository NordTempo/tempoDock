package core

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// prT0 is the updated_at of the first event in these tests.
var prT0 = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

// openPreview delivers an opened event for pull request n and fails
// unless it made pr-<n>.
func (h *harness) openPreview(appHook string, n int64) {
	h.t.Helper()
	msg, err := h.deliver(appHook, fmt.Sprintf("open-%d", n), prEvent{action: "opened", number: n, sha: shaA, updated: prT0})
	if err != nil || !strings.Contains(msg, fmt.Sprintf("preview pr-%d created", n)) {
		h.t.Fatalf("open #%d: %q, %v", n, msg, err)
	}
}

// previewLive waits until shop/<env> is a live preview.
func (h *harness) previewLive(env string) {
	h.t.Helper()
	waitFor(h.t, func() bool {
		pv, err := h.c.GetPreview(h.ctx, testSystem, EnvRef{"shop", env})
		return err == nil && pv.Status == apitypes.PreviewLive
	})
}

// envSnapshot is what a change to an environment would show: each
// service's releases and the environment's variables, revealed. It waits
// for env's deploys to settle first, so a release still health checking
// doesn't read as a change.
func (h *harness) envSnapshot(env string) string {
	h.t.Helper()
	h.settled(env)
	var b strings.Builder
	for _, svc := range []string{"web", "api", "db"} {
		rels, err := h.c.ListReleases(h.ctx, testSystem, ServiceRef{"shop", env, svc}, 50)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, r := range rels {
			fmt.Fprintf(&b, "%s %d %s\n", svc, r.Number, r.Status)
		}
	}
	vars, err := h.c.ListVariables(h.ctx, testSystem, EnvRef{"shop", env}, "", true)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, v := range vars {
		fmt.Fprintf(&b, "%s.%s=%s\n", v.Service, v.Key, deref(v.Value))
	}
	return b.String()
}

// previewVolumes is the Docker names of a preview environment's volumes,
// read from the store so it works after the archive too.
func (h *harness) previewVolumes(envID int64) []string {
	h.t.Helper()
	svcs, err := h.store.ListServices(h.ctx, envID)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, s := range svcs {
		vs, err := h.store.ListVolumes(h.ctx, s.ID)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, v := range vs {
			out = append(out, v.DockerName)
		}
	}
	return out
}

// settled waits until no release of shop/<env>'s services is in flight.
func (h *harness) settled(env string) {
	h.t.Helper()
	waitFor(h.t, func() bool {
		for _, svc := range []string{"web", "api", "db"} {
			rels, err := h.c.ListReleases(h.ctx, testSystem, ServiceRef{"shop", env, svc}, 50)
			if err != nil {
				h.t.Fatal(err)
			}
			for _, r := range rels {
				if !r.Status.Done() {
					return false
				}
			}
		}
		return true
	})
}

// envID is shop/<env>'s ID.
func (h *harness) envID(env string) int64 {
	h.t.Helper()
	es, err := h.c.loadEnv(h.ctx, 1, EnvRef{"shop", env})
	if err != nil {
		h.t.Fatal(err)
	}
	return es.env.ID
}

// waitArchived waits until environment id is archived and no volume in
// vols is left on the runtime (removed, or never made).
func (h *harness) waitArchived(id int64, vols []string) {
	h.t.Helper()
	waitFor(h.t, func() bool {
		e, err := h.store.GetEnvironment(h.ctx, id)
		return err == nil && e.ArchivedAt != nil
	})
	waitFor(h.t, func() bool {
		du, err := h.rt.DiskUsage(h.ctx, node.PruneImagesRequest{})
		if err != nil {
			h.t.Fatal(err)
		}
		return !slices.ContainsFunc(du.Volumes, func(v node.VolumeUsage) bool { return slices.Contains(vols, v.Name) })
	})
}

// A locked base still gets previews and stays as it was; the preview
// starts unlocked. A token the base's lock stops can't reach the base
// through previews, though it may close the unlocked preview.
func TestPreviewLockedBase(t *testing.T) {
	h, _, _, appHook := previewHarness(t)
	h.lock("staging")
	before := h.envSnapshot("staging")
	h.openPreview(appHook, 7)
	h.previewLive("pr-7")
	if env, err := h.c.GetEnvironment(h.ctx, testSystem, EnvRef{"shop", "pr-7"}); err != nil || env.Lock != nil {
		t.Fatalf("preview %+v, %v: starts unlocked", env.Lock, err)
	}
	agent := Actor{Kind: "token", OrgID: 1, Name: "agent", TokenID: 9, Scope: apitypes.ScopeAdmin}
	if _, err := h.c.Promote(h.ctx, agent, "shop", apitypes.PromoteRequest{From: "pr-7", To: "staging"}); CodeOf(err) != apitypes.CodeEnvironmentLocked {
		t.Fatalf("promote the preview into the locked base: %v", err)
	}
	if _, err := h.c.ClosePreview(h.ctx, agent, EnvRef{"shop", "staging"}); CodeOf(err) != apitypes.CodeEnvironmentLocked {
		t.Fatalf("close the locked base as a preview: %v", err)
	}
	if _, err := h.c.ClosePreview(h.ctx, agent, EnvRef{"shop", "pr-7"}); err != nil {
		t.Fatalf("close the unlocked preview: %v", err)
	}
	if after := h.envSnapshot("staging"); after != before {
		t.Fatalf("the base changed:\n%s\nwas\n%s", after, before)
	}
	if env, err := h.c.GetEnvironment(h.ctx, testSystem, EnvRef{"shop", "staging"}); err != nil || env.Lock == nil {
		t.Fatalf("base lock %+v, %v", env.Lock, err)
	}
}

// A preview locked for people too holds its next commit for approval as
// an access request, like any push into a frozen environment.
func TestPreviewLockedHoldsCommits(t *testing.T) {
	h, _, _, appHook := previewHarness(t)
	h.openPreview(appHook, 7)
	h.previewLive("pr-7")
	// Live counts the deployed services; web's first release comes from
	// its git fetch and may still be on its way.
	waitFor(t, func() bool {
		rels, err := h.c.ListReleases(h.ctx, testSystem, ServiceRef{"shop", "pr-7", "web"}, 10)
		return err == nil && len(rels) == 1 && rels[0].Status == apitypes.ReleaseActive
	})
	h.freeze("pr-7")
	if msg, err := h.deliver(appHook, "s1", prEvent{action: "synchronize", number: 7, sha: shaB, updated: prT0.Add(time.Minute)}); err != nil || !strings.Contains(msg, "updated to bbbbbbb") {
		t.Fatalf("synchronize: %q, %v", msg, err)
	}
	var reqs []apitypes.AccessRequest
	waitFor(t, func() bool {
		reqs, _ = h.c.ListRequests(h.ctx, testSystem, RequestFilter{Status: apitypes.RequestPending})
		return len(reqs) == 1
	})
	// Lists leave the arguments out; the request itself has them.
	r, err := h.c.GetRequest(h.ctx, testSystem, reqs[0].ID)
	if err != nil || r.Environment != "pr-7" || !strings.Contains(string(r.Args), shaB) {
		t.Fatalf("request %+v, %v", r, err)
	}
	if rels, err := h.c.ListReleases(h.ctx, testSystem, ServiceRef{"shop", "pr-7", "web"}, 10); err != nil || len(rels) != 1 || rels[0].Commit.SHA != shaA {
		t.Fatalf("web deployed the held commit: %+v, %v", rels, err)
	}
}

// A preview shares nothing with its base it wasn't given: other volumes,
// addresses of its own, no unlisted literal or org reference. A token
// limited to the base can't see or close it.
func TestPreviewIsolation(t *testing.T) {
	h, _, _, appHook := previewHarness(t)
	h.set(EnvRef{"shop", "staging"}, "", map[string]string{"ORG": "${{org.PROD_DB}}"}, false)
	h.set(EnvRef{"shop", "staging"}, "api", map[string]string{"KEY": "sk_live"}, true)
	h.openPreview(appHook, 7)
	h.previewLive("pr-7")

	baseVols, prVols := h.previewVolumes(h.envID("staging")), h.previewVolumes(h.envID("pr-7"))
	if len(baseVols) == 0 || len(prVols) != len(baseVols) {
		t.Fatalf("volumes: base %v, preview %v", baseVols, prVols)
	}
	for _, v := range prVols {
		if slices.Contains(baseVols, v) {
			t.Fatalf("the preview mounts the base's volume %s", v)
		}
	}
	base, _ := h.c.ListDomains(h.ctx, testSystem, ServiceRef{"shop", "staging", "web"})
	pr, _ := h.c.ListDomains(h.ctx, testSystem, ServiceRef{"shop", "pr-7", "web"})
	if len(base) != 1 || len(pr) != 1 || pr[0].Hostname == base[0].Hostname {
		t.Fatalf("domains: base %+v, preview %+v", base, pr)
	}
	vars, _ := h.c.ListVariables(h.ctx, testSystem, EnvRef{"shop", "pr-7"}, "", true)
	for _, v := range vars {
		if val := deref(v.Value); strings.Contains(val, "org.") || val == "sk_live" || val == "postgres://prod" {
			t.Fatalf("%s.%s=%s reached the preview", v.Service, v.Key, val)
		}
	}

	limited := Actor{Kind: "token", OrgID: 1, Name: "staging-only", TokenID: 9, Scope: apitypes.ScopeAdmin, EnvironmentID: h.envID("staging")}
	if s, err := h.c.GetPreviews(h.ctx, limited, "shop"); err != nil || len(s.Previews) != 0 {
		t.Fatalf("a token limited to staging sees previews: %+v, %v", s.Previews, err)
	}
	if _, err := h.c.ClosePreview(h.ctx, limited, EnvRef{"shop", "pr-7"}); err == nil {
		t.Fatal("a token limited to staging closed the preview")
	}
	inPreview := Actor{Kind: "token", OrgID: 1, Name: "pr-only", TokenID: 10, Scope: apitypes.ScopeDeploy, EnvironmentID: h.envID("pr-7")}
	if _, err := h.c.ClosePreview(h.ctx, inPreview, EnvRef{"shop", "pr-7"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a token limited to the preview closed it: %v", err)
	}
}

// Deliveries for different pull requests arriving at once never open
// more previews than the cap; closing one frees a slot. Two deliveries
// of one pull request at once make one preview.
func TestPreviewCapUnderConcurrency(t *testing.T) {
	h, _, _, appHook := previewHarness(t) // at most 2
	deliverAll := func(evs map[string]prEvent) []string {
		var mu sync.Mutex
		var wg sync.WaitGroup
		var msgs []string
		for id, ev := range evs {
			wg.Go(func() {
				msg, err := h.deliver(appHook, id, ev)
				if err != nil {
					t.Errorf("%s: %v", id, err)
				}
				mu.Lock()
				msgs = append(msgs, msg)
				mu.Unlock()
			})
		}
		wg.Wait()
		return msgs
	}
	evs := map[string]prEvent{}
	for n := int64(10); n < 15; n++ {
		evs[fmt.Sprint("c", n)] = prEvent{action: "opened", number: n, sha: shaA, updated: prT0}
	}
	created := 0
	for _, m := range deliverAll(evs) {
		if strings.Contains(m, "created") {
			created++
		}
	}
	open, err := h.store.CountOpenPreviews(h.ctx, projectID(t, h))
	if created != 2 || err != nil || open != 2 {
		t.Fatalf("%d created, %d open (%v); the cap is 2", created, open, err)
	}
	s, _ := h.c.GetPreviews(h.ctx, testSystem, "shop")
	if _, err := h.c.ClosePreview(h.ctx, testSystem, EnvRef{"shop", s.Previews[0].Environment}); err != nil {
		t.Fatal(err)
	}
	same := deliverAll(map[string]prEvent{
		"s1": {action: "opened", number: 20, sha: shaA, updated: prT0},
		"s2": {action: "opened", number: 20, sha: shaA, updated: prT0},
	})
	if n := strings.Count(strings.Join(same, "\n"), "preview pr-20 created"); n != 1 {
		t.Fatalf("two deliveries of #20: %q", same)
	}
	envs, _ := h.c.ListEnvironments(h.ctx, testSystem, "shop")
	if n := len(slices.DeleteFunc(envs, func(e apitypes.Environment) bool { return !strings.HasPrefix(e.Name, "pr-20") })); n != 1 {
		t.Fatalf("%d environments for #20", n)
	}
}

// projectID is shop's ID.
func projectID(t *testing.T, h *harness) int64 {
	t.Helper()
	p, err := h.store.GetProjectByName(h.ctx, store.GetProjectByNameParams{OrgID: 1, Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// Closed during a deploy, a preview is archived once the deploy ends,
// volumes and hostname freed.
func TestPreviewCloseDuringDeploy(t *testing.T) {
	h, _, _, appHook := previewHarness(t)
	release := h.rt.HoldHealthChecks()
	defer release()
	h.openPreview(appHook, 7)
	id := h.envID("pr-7")
	waitFor(t, func() bool {
		pv, err := h.c.GetPreview(h.ctx, testSystem, EnvRef{"shop", "pr-7"})
		return err == nil && pv.Status == apitypes.PreviewDeploying
	})
	vols := h.previewVolumes(id)
	host := h.generatedHost(ServiceRef{"shop", "pr-7", "web"})
	if _, err := h.c.ClosePreview(h.ctx, testSystem, EnvRef{"shop", "pr-7"}); err != nil {
		t.Fatal(err)
	}
	if e, _ := h.store.GetEnvironment(h.ctx, id); e.ArchivedAt != nil {
		t.Fatal("archived while a deploy runs")
	}
	release()
	h.waitArchived(id, vols)
	if exists, _ := h.store.HostnameExists(h.ctx, host); exists {
		t.Fatal("the hostname is still taken")
	}
}

// A restart finishes a closed preview's archive that waited for a deploy
// the restart cut short.
func TestPreviewArchiveResumesAfterRestart(t *testing.T) {
	h, _, _, appHook := previewHarness(t)
	release := h.rt.HoldHealthChecks()
	defer release()
	h.openPreview(appHook, 7)
	id := h.envID("pr-7")
	waitFor(t, func() bool {
		pv, err := h.c.GetPreview(h.ctx, testSystem, EnvRef{"shop", "pr-7"})
		return err == nil && pv.Status == apitypes.PreviewDeploying
	})
	vols := h.previewVolumes(id)
	if _, err := h.c.ClosePreview(h.ctx, testSystem, EnvRef{"shop", "pr-7"}); err != nil {
		t.Fatal(err)
	}
	// The daemon stops mid-deploy; the next one fails the deploy in
	// recovery and archives the preview, volumes made so far included.
	h.c.Close()
	release()
	h.c = h.start()
	h.waitArchived(id, vols)
}

// A restart while an archived preview's containers are still stopping
// still removes its volumes: the new daemon finishes the teardown.
func TestPreviewVolumesRemovedAfterRestartMidTeardown(t *testing.T) {
	h, _, _, appHook := previewHarness(t)
	h.openPreview(appHook, 7)
	h.previewLive("pr-7")
	id := h.envID("pr-7")
	vols := h.previewVolumes(id)
	release := h.rt.HoldStops()
	if _, err := h.c.ClosePreview(h.ctx, testSystem, EnvRef{"shop", "pr-7"}); err != nil {
		t.Fatal(err)
	}
	h.c.Close() // the held stops fail with the old daemon's context
	release()
	h.c = h.start()
	h.waitArchived(id, vols)
	// Nothing is left to remove at the next start.
	waitFor(t, func() bool {
		left, err := h.store.ListArchivedPreviewVolumes(h.ctx)
		return err == nil && len(left) == 0
	})
}

// A project whose preview was closed archives: the closed preview's
// archived environment and services are skipped, not looked up.
func TestArchiveProjectAfterAClosedPreview(t *testing.T) {
	h, _, _, appHook := previewHarness(t)
	h.openPreview(appHook, 7)
	h.previewLive("pr-7")
	id := h.envID("pr-7")
	vols := h.previewVolumes(id)
	if _, err := h.c.ClosePreview(h.ctx, testSystem, EnvRef{"shop", "pr-7"}); err != nil {
		t.Fatal(err)
	}
	h.waitArchived(id, vols)
	// The harness's own staging deploys may still run; archiving waits
	// for them by refusing.
	waitFor(t, func() bool {
		rels, err := h.store.ListInFlightReleases(h.ctx)
		return err == nil && len(rels) == 0
	})
	p, err := h.c.ArchiveProject(h.ctx, testSystem, "shop")
	if err != nil || p.ArchivedAt == nil {
		t.Fatalf("archive shop = %+v, %v", p, err)
	}
}
