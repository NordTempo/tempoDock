package core

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// sourceTar is a small build context.
func sourceTar(body string) *bytes.Buffer {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "server.js", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	return &buf
}

// heldFiles lists the archives held for requests.
func (h *harness) heldFiles() []string {
	h.t.Helper()
	entries, _ := os.ReadDir(filepath.Join(h.opts.Config.DataDir, "requests"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestHeldSourceDeploy: a token's upload into a locked environment is
// checked and kept as a request, a newer upload replaces it, and approving
// deploys the newer one; its archive goes with the request.
func TestHeldSourceDeploy(t *testing.T) {
	h := newRequestHarness(t)
	web := ServiceRef{"shop", "production", "web"}
	_, err := h.c.DeploySource(h.ctx, h.agent, web, sourceTar("v1"), 1<<20)
	first, ok := errors.AsType[*LockedError](err)
	if !ok || first.Request == 0 || len(h.heldFiles()) != 1 {
		t.Fatalf("held: %v, files %v", err, h.heldFiles())
	}
	if rels, _ := h.c.ListReleases(h.ctx, testSystem, web, 10); len(rels) != 0 {
		t.Fatalf("released before approval: %+v", rels)
	}
	_, err = h.c.DeploySource(WithReason(h.ctx, "v2 fixes it"), h.agent, web, sourceTar("v2"), 1<<20)
	second, _ := errors.AsType[*LockedError](err)
	if second == nil || second.Request != first.Request || len(h.heldFiles()) != 1 {
		t.Fatalf("replaced: %v, files %v", err, h.heldFiles())
	}
	r, _ := h.c.GetRequest(h.ctx, h.admin, first.Request)
	var args heldDeploy
	_ = json.Unmarshal(r.Args, &args)
	if r.Reason != "v2 fixes it" || r.Via != "source" || args.Bytes == 0 || args.Upload != h.heldFiles()[0] {
		t.Fatalf("request: %+v %+v", r, args)
	}
	out, err := h.c.DecideRequest(h.ctx, h.admin, first.Request, true, apitypes.DecideAccessRequest{})
	if err != nil || out.Status != apitypes.RequestDone {
		t.Fatalf("approve: %+v %v", out, err)
	}
	rel := h.wait(web, 1)
	if rel.Status != apitypes.ReleaseActive || rel.CreatedBy != "claude" || len(h.heldFiles()) != 0 {
		t.Fatalf("release %+v, files %v", rel, h.heldFiles())
	}
}

// TestHeldSourceRejected: rejecting a held upload removes its archive.
func TestHeldSourceRejected(t *testing.T) {
	h := newRequestHarness(t)
	_, err := h.c.DeploySource(h.ctx, h.agent, ServiceRef{"shop", "production", "web"}, sourceTar("v1"), 1<<20)
	le, _ := errors.AsType[*LockedError](err)
	if le == nil {
		t.Fatal(err)
	}
	if _, err := h.c.DecideRequest(h.ctx, h.admin, le.Request, false, apitypes.DecideAccessRequest{}); err != nil {
		t.Fatal(err)
	}
	if files := h.heldFiles(); len(files) != 0 {
		t.Fatalf("files after reject: %v", files)
	}
}

// TestHeldPush: a GitHub push into a locked environment waits as an
// access request (the newest push replacing an older one) and deploys
// once approved; a person's Deploy isn't held.
func TestHeldPush(t *testing.T) {
	h, _ := gitHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	k := h.connect(web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main"})
	h.lock("production")
	admin := Actor{Kind: "user", OrgID: 1, Name: "saile@example.com", UserID: 1, Scope: apitypes.ScopeAdmin}

	pending := func() []apitypes.AccessRequest {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			rs, err := h.c.ListRequests(h.ctx, admin, RequestFilter{Status: apitypes.RequestPending, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			src, _ := h.c.GitStatus(h.ctx, testSystem, web)
			if len(rs) > 0 && src.Connection != nil && strings.Contains(src.Connection.LastEvent, "waits for approval") {
				return rs
			}
			if time.Now().After(deadline) {
				t.Fatalf("no held push: %+v %+v", rs, src.Connection)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if _, err := h.push(k, pushBody("refs/heads/main", shaA, "server.js")); err != nil {
		t.Fatal(err)
	}
	first := pending()
	if _, err := h.push(k, pushBody("refs/heads/main", shaB, "server.js")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, _ := h.c.GetRequest(h.ctx, admin, first[0].ID)
		if strings.Contains(string(r.Args), shaB) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("newer push didn't replace it: %s", r.Args)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rs := pending(); len(rs) != 1 || rs[0].ID != first[0].ID || rs[0].RequestedBy != "github:octocat" {
		t.Fatalf("pending: %+v", rs)
	}
	if rels, _ := h.c.ListReleases(h.ctx, testSystem, web, 10); len(rels) != 0 {
		t.Fatalf("deployed before approval: %+v", rels)
	}
	if _, err := h.c.DecideRequest(h.ctx, admin, first[0].ID, true, apitypes.DecideAccessRequest{}); err != nil {
		t.Fatal(err)
	}
	r := h.wait(web, h.waitReleases(web, 1)[0].Number)
	if r.Status != apitypes.ReleaseActive || r.Commit == nil || r.Commit.SHA != shaB {
		t.Fatalf("release: %+v", r)
	}

	// A person deploying from the UI isn't held.
	if _, err := h.c.GitDeploy(h.ctx, admin, web, apitypes.GitDeployRequest{Ref: shaA}); err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, h.waitReleases(web, 2)[0].Number); r.Commit == nil || r.Commit.SHA != shaA {
		t.Fatalf("UI deploy: %+v", r)
	}
}

// TestHeldSourceReplaceDecided: a newer upload that found the pending
// request just before an admin decided it files a request of its own
// instead of failing and deleting its archive.
func TestHeldSourceReplaceDecided(t *testing.T) {
	h := newRequestHarness(t)
	web := ServiceRef{"shop", "production", "web"}
	_, err := h.c.DeploySource(h.ctx, h.agent, web, sourceTar("v1"), 1<<20)
	le, _ := errors.AsType[*LockedError](err)
	if le == nil {
		t.Fatal(err)
	}
	stale, err := h.c.store.GetAccessRequest(h.ctx, le.Request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.DecideRequest(h.ctx, h.admin, le.Request, false, apitypes.DecideAccessRequest{}); err != nil {
		t.Fatal(err)
	}
	// The newer upload, as fileRequest had it when it found stale pending.
	name := "newer.tar"
	if err := os.WriteFile(filepath.Join(h.opts.Config.DataDir, "requests", name), sourceTar("v2").Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	org, _ := h.c.store.GetOrg(h.ctx, le.OrgID)
	nonce := randomHex(16)
	sealed, _ := h.c.box.Seal([]byte(`{}`), sealedRequestArgs.AAD(nonce))
	call := Call{Via: "source", Op: "deploy-source", Title: "Deploy source", Args: json.RawMessage(`{}`), Key: "service 1", Upload: name}
	r, err := h.c.replaceRequest(h.ctx, h.agent, stale, le, call, nonce, sealed, org)
	if err != nil || r.ID == le.Request || r.Status != "pending" || r.Upload != name {
		t.Fatalf("replace a decided request: %+v %v", r, err)
	}
	if files := h.heldFiles(); len(files) != 1 || files[0] != name {
		t.Fatalf("held files: %v", files)
	}
}
