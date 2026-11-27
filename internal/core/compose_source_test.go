package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// An environment follows a Compose file through the GitHub App: following
// syncs the branch's head and stages its changes; a push changing the
// file queues a sync that stages the next version, with a removal only
// once prune is on (admin), and a push not touching it is ignored.
// Auto-apply applies a sync that only adds and changes, never one that
// removes.
func TestComposeFollow(t *testing.T) {
	h, gh, appHook := appHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "web:1"})
	env := web.Env()
	setFile := func(content string) {
		gh.mu.Lock()
		gh.files["deploy/compose.yaml"] = content
		gh.mu.Unlock()
	}
	setFile("services:\n  web:\n    image: web:1\n  worker:\n    image: worker:1\n    environment:\n      API_TOKEN: tok-1\n")

	deployer := Actor{Name: "ci", Kind: "token", OrgID: 1, Scope: apitypes.ScopeDeploy}
	if _, err := h.c.FollowCompose(h.ctx, deployer, env, apitypes.ComposeSourceRequest{Repo: "acme/shop", Path: "deploy/compose.yaml", Prune: true}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a deploy token turned on prune: %v", err)
	}
	if _, err := h.c.FollowCompose(h.ctx, testSystem, env, apitypes.ComposeSourceRequest{Repo: "acme/shop", Path: "deploy/missing.yaml"}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("a missing file: %v", err)
	}
	res, err := h.c.FollowCompose(h.ctx, deployer, env, apitypes.ComposeSourceRequest{Repo: "acme/shop", Path: "deploy/compose.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	last := res.Source.Last
	if res.Source.Branch != "main" || last == nil || last.Outcome != apitypes.ComposeStaged || last.Commit.SHA != shaC || last.Summary != "+1 service, 2 other changes" {
		t.Fatalf("first sync: %+v %+v", res.Source, last)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), "tok-1") {
		t.Fatalf("the sync shows a secret: %s", b)
	}
	if _, err := h.c.ApplyChanges(h.ctx, testSystem, env); err != nil {
		t.Fatal(err)
	}
	worker := ServiceRef{"shop", "production", "worker"}
	if vars := h.revealed(worker); vars["API_TOKEN"] != "tok-1" {
		t.Fatalf("worker's variables = %v", vars)
	}
	// A secret set in Dock since keeps its value at the next sync.
	h.set(env, "worker", map[string]string{"API_TOKEN": "rotated"}, true)

	n := 0
	pushAt := func(sha string, files ...string) string {
		n++
		var p map[string]any
		_ = json.Unmarshal(pushBody("refs/heads/main", sha, files...), &p)
		p["repository"] = map[string]any{"id": fakeRepoID, "full_name": "acme/shop"}
		p["installation"] = map[string]any{"id": fakeInstall}
		body, _ := json.Marshal(p)
		msg, err := h.c.GitWebhook(h.ctx, GitDelivery{HookID: appHook, Event: "push", ID: fmt.Sprintf("c%d", n), Signature: sign("app-secret", body), Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	push := func(files ...string) string { return pushAt(shaA, files...) }
	synced := func(sha string) apitypes.ComposeSync {
		t.Helper()
		var got apitypes.ComposeSourceStatus
		waitFor(t, func() bool {
			got, err = h.c.ComposeSource(h.ctx, testSystem, env)
			return err == nil && got.Source.Pending == "" && got.Source.Last != nil && got.Source.Last.Commit.SHA == sha
		})
		return *got.Source.Last
	}
	if msg := push("README.md"); !strings.Contains(msg, "shop/production (Compose): push aaaaaaa: ignored, deploy/compose.yaml didn't change") {
		t.Fatalf("unrelated push: %q", msg)
	}

	// The next version bumps web only: worker's secret isn't restaged.
	setFile("services:\n  web:\n    image: web:2\n  worker:\n    image: worker:1\n    environment:\n      API_TOKEN: tok-1\n")
	if msg := push("deploy/compose.yaml"); !strings.Contains(msg, "shop/production (Compose): push aaaaaaa: queued") {
		t.Fatalf("push: %q", msg)
	}
	if s := synced(shaA); s.Outcome != apitypes.ComposeStaged || s.Summary != "1 other change" {
		t.Fatalf("sync: %+v", s)
	}

	// Dropping worker: kept without prune; with prune (and auto-apply)
	// its archive is staged, never applied on its own.
	setFile("services:\n  web:\n    image: web:2\n")
	if _, err := h.c.SyncCompose(h.ctx, deployer, env); err != nil {
		t.Fatal(err)
	}
	got, _ := h.c.ComposeSource(h.ctx, testSystem, env)
	if s := got.Source.Last; s.Outcome != apitypes.ComposeUnchanged {
		t.Fatalf("sync without prune: %+v", s)
	}
	if _, err := h.c.FollowCompose(h.ctx, testSystem, env, apitypes.ComposeSourceRequest{Repo: "acme/shop", Path: "deploy/compose.yaml", Prune: true, AutoApply: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = h.c.ComposeSource(h.ctx, testSystem, env)
	if s := got.Source.Last; s.Outcome != apitypes.ComposeStaged || !strings.Contains(s.Summary, "−1 service") || !strings.Contains(s.Summary, "not applied: it removes something") {
		t.Fatalf("sync with prune: %+v", s)
	}
	// worker's own releases settle first: an archive waits for a deploy.
	wrels := h.waitReleases(worker, 1)
	h.wait(worker, wrels[0].Number)
	if _, err := h.c.ApplyChanges(h.ctx, testSystem, env); err != nil {
		t.Fatal(err)
	}
	if s, _ := h.c.GetService(h.ctx, testSystem, worker); s.ArchivedAt == nil {
		t.Fatal("worker wasn't archived")
	}
	// Its release settles first: applying skips a service that is deploying.
	rels := h.waitReleases(web, 1)
	h.wait(web, rels[0].Number)

	// Only additions and changes, nothing else waiting: applied.
	setFile("services:\n  web:\n    image: web:3\n")
	pushAt(shaB, "deploy/compose.yaml")
	if s := synced(shaB); s.Outcome != apitypes.ComposeApplied {
		t.Fatalf("auto-apply: %+v", s)
	}
	if p, _ := h.c.GetChanges(h.ctx, testSystem, env, false); p.Count != 0 {
		t.Fatalf("left staged: %+v", p)
	}

	if err := h.c.UnfollowCompose(h.ctx, testSystem, env); err != nil {
		t.Fatal(err)
	}
	if got, err := h.c.ComposeSource(h.ctx, testSystem, env); err != nil || got.Source != nil {
		t.Fatalf("after unfollowing: %+v, %v", got, err)
	}
}
