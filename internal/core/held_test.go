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
	if second == nil || second.Request == first.Request || len(h.heldFiles()) != 1 {
		t.Fatalf("replaced: %v, files %v", err, h.heldFiles())
	}
	// An admin who reviewed the first upload can't approve the second.
	if _, err := h.c.DecideRequest(h.ctx, h.admin, first.Request, true, apitypes.DecideAccessRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("approve replaced request: %v", err)
	}
	if rels, _ := h.c.ListReleases(h.ctx, testSystem, web, 10); len(rels) != 0 {
		t.Fatalf("replaced request released: %+v", rels)
	}
	r, _ := h.c.GetRequest(h.ctx, h.admin, second.Request)
	var args heldDeploy
	_ = json.Unmarshal(r.Args, &args)
	if r.Reason != "v2 fixes it" || r.Via != "source" || args.Bytes == 0 || args.Upload != h.heldFiles()[0] {
		t.Fatalf("request: %+v %+v", r, args)
	}
	out, err := h.c.DecideRequest(h.ctx, h.admin, second.Request, true, apitypes.DecideAccessRequest{})
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

// TestHeldSourceEnded: a held upload's archive goes when its request
// expires or its token is revoked.
func TestHeldSourceEnded(t *testing.T) {
	h := newRequestHarness(t)
	web := ServiceRef{"shop", "production", "web"}
	_, err := h.c.DeploySource(h.ctx, h.agent, web, sourceTar("v1"), 1<<20)
	le, _ := errors.AsType[*LockedError](err)
	if le == nil || len(h.heldFiles()) != 1 {
		t.Fatalf("held: %v", err)
	}
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE access_requests SET expires_at = ? WHERE id = ?", now().Add(-time.Minute), le.Request); err != nil {
		t.Fatal(err)
	}
	if err := h.c.expireRequests(h.ctx, now()); err != nil {
		t.Fatal(err)
	}
	if files := h.heldFiles(); len(files) != 0 {
		t.Fatalf("files after expiry: %v", files)
	}

	if _, err := h.c.DeploySource(h.ctx, h.agent, web, sourceTar("v2"), 1<<20); len(h.heldFiles()) != 1 {
		t.Fatalf("held again: %v", err)
	}
	if err := h.c.RevokeToken(h.ctx, testSystem, h.agent.TokenID); err != nil {
		t.Fatal(err)
	}
	if files := h.heldFiles(); len(files) != 0 {
		t.Fatalf("files after revoke: %v", files)
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
	// The newer push is filed as a new request, so approving the one an
	// admin reviewed can't run the newer commit.
	var newer apitypes.AccessRequest
	deadline := time.Now().Add(5 * time.Second)
	for {
		rs := pending()
		if len(rs) == 1 && rs[0].ID != first[0].ID {
			newer = rs[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("newer push didn't replace it: %+v", rs)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if newer, _ = h.c.GetRequest(h.ctx, admin, newer.ID); !strings.Contains(string(newer.Args), shaB) || newer.RequestedBy != "github:octocat" {
		t.Fatalf("pending: %+v", newer)
	}
	if _, err := h.c.DecideRequest(h.ctx, admin, first[0].ID, true, apitypes.DecideAccessRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("approve replaced request: %v", err)
	}
	if rels, _ := h.c.ListReleases(h.ctx, testSystem, web, 10); len(rels) != 0 {
		t.Fatalf("deployed before approval: %+v", rels)
	}
	if _, err := h.c.DecideRequest(h.ctx, admin, newer.ID, true, apitypes.DecideAccessRequest{}); err != nil {
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
