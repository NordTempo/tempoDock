package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
	"github.com/NordTempo/tempoDock/internal/update"
	"github.com/NordTempo/tempoDock/internal/version"
)

// countingReleases counts release checks.
type countingReleases struct {
	update.Source
	checks int
}

func (c *countingReleases) Releases(ctx context.Context) ([]update.Release, error) {
	c.checks++
	return c.Source.Releases(ctx)
}

// writeRelease adds release v to a directory source, for this machine's
// arch and arm64.
func writeRelease(t *testing.T, dir, v string) {
	t.Helper()
	rel := filepath.Join(dir, v)
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	for _, name := range update.Assets(v, runtime.GOARCH, "arm64")[1:] {
		b := []byte(name + " of " + v)
		if err := os.WriteFile(filepath.Join(rel, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	for name, b := range map[string]string{update.SumsAsset: sums.String(), "notes.md": "## " + v + " notes"} {
		if err := os.WriteFile(filepath.Join(rel, name), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// updatePool is a control plane "cp" on v1.0.0 with a node "homelab"
// (arm64, on v0.9.0), releases v0.9.0 and v1.1.0 in a directory, and the
// updater installed.
func updatePool(t *testing.T) (*pool, *countingReleases) {
	t.Helper()
	setVersion(t, "v1.0.0")
	p := newPool(t, poolServer{"cp", true, true, 8}, poolServer{"homelab", false, true, 8})
	releases := t.TempDir()
	writeRelease(t, releases, "v0.9.0")
	writeRelease(t, releases, "v1.1.0")
	src := &countingReleases{Source: update.DirSource{Path: releases}}
	unit := filepath.Join(t.TempDir(), "dock-update.path")
	if err := os.WriteFile(unit, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p.opts.Updates, p.opts.UpdaterUnit = src, unit
	p.nodes.add(p.ids["homelab"], p.rts[p.ids["homelab"]], remote.Hello{
		Version: "v0.9.0", Protocol: remote.UpdateProtocol, Arch: "arm64", Facts: remote.Facts{Ingress: remote.IngressDirect},
	})
	p.restart()
	return p, src
}

// setVersion makes this binary v for the test.
func setVersion(t *testing.T, v string) {
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

func (h *harness) platformAudit(action string) []apitypes.AuditEvent {
	h.t.Helper()
	evs, err := h.c.ListAudit(h.ctx, System, AuditFilter{Action: action, Platform: true})
	if err != nil {
		h.t.Fatal(err)
	}
	return evs
}

func TestUpdateStatus(t *testing.T) {
	p, src := updatePool(t)
	if _, err := p.c.UpdateStatus(p.ctx, p.user("dev@x", apitypes.ScopeAdmin), false); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin who isn't an operator: %v", err)
	}
	st, err := p.c.UpdateStatus(p.ctx, testSystem, false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "v1.0.0" || st.Latest != "v1.1.0" || !st.Available || !st.Updater || st.CheckError != "" {
		t.Fatalf("status %+v", st)
	}
	if len(st.Releases) != 1 || st.Releases[0].Version != "v1.1.0" || st.Releases[0].Notes != "## v1.1.0 notes" {
		t.Errorf("releases %+v; want only the newer one, with its notes", st.Releases)
	}
	for _, s := range st.Servers {
		if behind := s.Name == "homelab"; s.Behind != behind || s.Manual != "" {
			t.Errorf("server %+v", s)
		}
	}
	// The check is reused until refreshed.
	if _, err := p.c.UpdateStatus(p.ctx, testSystem, false); err != nil || src.checks != 1 {
		t.Errorf("second status: %d checks, %v", src.checks, err)
	}
	if _, err := p.c.UpdateStatus(p.ctx, testSystem, true); err != nil || src.checks != 2 {
		t.Errorf("refresh: %d checks, %v", src.checks, err)
	}
}

// The control plane stages the release for itself and its nodes, asks the
// updater, and the new version it restarts as audits the result and
// updates the node.
func TestUpdateControlPlaneThenNodes(t *testing.T) {
	p, _ := updatePool(t)
	st, err := p.c.StartUpdate(p.ctx, testSystem, apitypes.StartUpdateRequest{})
	if err != nil || st.Running == nil || st.Running.Version != "v1.1.0" {
		t.Fatalf("StartUpdate = %+v, %v", st.Running, err)
	}
	if _, err := p.c.StartUpdate(p.ctx, testSystem, apitypes.StartUpdateRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("a second update: %v", err)
	}
	dir := update.Dir(p.opts.Config.DataDir)
	var req update.Request
	eventually(t, "the request", func() bool { return update.ReadJSON(dir, update.RequestFile, &req) == nil })
	if req.Version != "v1.1.0" || !req.Servers || req.RequestedBy != testSystem.Name {
		t.Errorf("request %+v", req)
	}
	for _, arch := range []string{runtime.GOARCH, "arm64"} {
		if _, err := os.Stat(filepath.Join(update.ReleaseDir(p.opts.Config.DataDir, "v1.1.0"), update.BinaryAsset(arch))); err != nil {
			t.Errorf("not staged for %s: %v", arch, err)
		}
	}
	if len(p.platformAudit("platform.update_started")) != 1 {
		t.Error("platform.update_started not audited")
	}

	// The updater takes the request, updates and restarts the daemon.
	if err := os.Rename(filepath.Join(dir, update.RequestFile), filepath.Join(dir, update.ApplyingFile)); err != nil {
		t.Fatal(err)
	}
	p.c.Close()
	if err := update.WriteJSON(dir, update.ResultFile, update.Result{
		Request: req, From: "v1.0.0", Status: update.Succeeded, StartedAt: now(), FinishedAt: now(), Backup: "/var/lib/dock-update/backup.tar.gz",
	}, nil); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, update.ApplyingFile))
	version.Version = "v1.1.0"
	p.c = p.start()

	eventually(t, "the node update", func() bool { return len(p.platformAudit("server.updated")) == 1 })
	if evs := p.platformAudit("platform.updated"); len(evs) != 1 || !strings.Contains(evs[0].Summary, "from v1.0.0 to v1.1.0") {
		t.Errorf("platform.updated %+v", evs)
	}
	sent := p.nodes.updated[p.ids["homelab"]]
	if len(sent) != 1 || sent[0].Version != "v1.1.0" || sent[0].Sums[update.BinaryAsset("arm64")] == "" {
		t.Errorf("node updates %+v", sent)
	}
	eventually(t, "the update to end", func() bool {
		st, err := p.c.UpdateStatus(p.ctx, testSystem, false)
		return err == nil && st.Running == nil && st.Last != nil && st.Last.Status == "succeeded"
	})
	// Taken once: a restart doesn't audit it again.
	p.restart()
	if len(p.platformAudit("platform.updated")) != 1 {
		t.Error("the result was audited twice")
	}
}

// A rolled-back update is audited as failed, and the old version doesn't
// go on to the nodes.
func TestUpdateRolledBack(t *testing.T) {
	p, _ := updatePool(t)
	dir := update.Dir(p.opts.Config.DataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := update.WriteJSON(dir, update.ResultFile, update.Result{
		Request: update.Request{Version: "v1.1.0", RequestedBy: "saile", Servers: true}, From: "v1.0.0",
		Status: update.RolledBack, Error: "dock v1.1.0 isn't healthy", StartedAt: now(), FinishedAt: now(),
	}, nil); err != nil {
		t.Fatal(err)
	}
	p.c.UpdateFinished(p.ctx)
	if evs := p.platformAudit("platform.update_failed"); len(evs) != 1 || !strings.Contains(evs[0].Summary, "rolled back") {
		t.Errorf("platform.update_failed %+v", evs)
	}
	st, err := p.c.UpdateStatus(p.ctx, testSystem, false)
	if err != nil || st.Running != nil || st.Last == nil || st.Last.Status != "rolled_back" {
		t.Fatalf("status %+v, %v", st, err)
	}
	if n := len(p.nodes.updated[p.ids["homelab"]]); n != 0 {
		t.Errorf("%d node updates after a rollback", n)
	}
}

// With the control plane on the version, only the nodes behind it are
// updated; one that fails is audited, an old one is skipped with what to
// run on it.
func TestUpdateNodesOnly(t *testing.T) {
	p, _ := updatePool(t)
	version.Version = "v1.1.0"
	old, err := p.store.CreateServer(p.ctx, store.CreateServerParams{Name: "old-box"})
	if err != nil {
		t.Fatal(err)
	}
	p.nodes.add(old.ID, p.rts[p.ids["homelab"]], remote.Hello{Version: "v0.8.0", Protocol: 3, Facts: remote.Facts{Ingress: remote.IngressDirect}})
	p.nodes.updateErr[p.ids["homelab"]] = errors.New("server homelab came back on v0.9.0")

	if _, err := p.c.StartUpdate(p.ctx, testSystem, apitypes.StartUpdateRequest{Version: "v1.1.0"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the node update to fail", func() bool { return len(p.platformAudit("server.update_failed")) == 1 })
	var st apitypes.UpdateStatus
	eventually(t, "the update to end", func() bool {
		st, err = p.c.UpdateStatus(p.ctx, testSystem, false)
		return err == nil && st.Running == nil
	})
	states := map[string]apitypes.UpdateServer{}
	for _, s := range st.Servers {
		states[s.Name] = s
	}
	if s := states["homelab"]; s.State != "failed" || !strings.Contains(s.Error, "came back on v0.9.0") {
		t.Errorf("homelab %+v", s)
	}
	if s := states["old-box"]; s.State != "skipped" || !strings.Contains(s.Error, "install.sh") || s.Manual == "" {
		t.Errorf("old-box %+v", s)
	}
	if update.Exists(update.Dir(p.opts.Config.DataDir), update.RequestFile) {
		t.Error("the control plane asked its updater for the version it runs")
	}
}

// Everything on the version already: nothing starts.
func TestUpdateUpToDate(t *testing.T) {
	p, _ := updatePool(t)
	version.Version = "v0.9.0"
	st, err := p.c.StartUpdate(p.ctx, testSystem, apitypes.StartUpdateRequest{Version: "v0.9.0"})
	if err != nil || st.Running != nil {
		t.Fatalf("StartUpdate = %+v, %v", st.Running, err)
	}
	if len(p.platformAudit("platform.update_started")) != 0 {
		t.Error("an update that does nothing was audited")
	}
}

func TestStartUpdateRefuses(t *testing.T) {
	p, _ := updatePool(t)
	if _, err := p.c.StartUpdate(p.ctx, testSystem, apitypes.StartUpdateRequest{Version: "v0.9.0"}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("a downgrade: %v", err)
	}
	if _, err := p.c.StartUpdate(p.ctx, testSystem, apitypes.StartUpdateRequest{Version: "latest; rm -rf /"}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("not a version: %v", err)
	}
	if err := os.Remove(p.opts.UpdaterUnit); err != nil {
		t.Fatal(err)
	}
	if _, err := p.c.StartUpdate(p.ctx, testSystem, apitypes.StartUpdateRequest{}); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "by hand") {
		t.Errorf("without the updater: %v", err)
	}
	version.Version = "dev"
	if _, err := p.c.StartUpdate(p.ctx, testSystem, apitypes.StartUpdateRequest{}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("a development build: %v", err)
	}
}
