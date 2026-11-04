package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// After a database is restored from an older backup, the next release gets
// an ID and number a still-running container already carries. The deploy
// fails before changing anything and leaves that container alone: it may
// be what serves the domains.
func TestDeployLeavesContainersFromBeforeARestore(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	first := h.deploy(web, "app:1")
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	// What the restored database doesn't know: release 2, started before.
	orphan, err := h.rt.Start(h.ctx, node.ContainerSpec{Name: containerName(ss, 2), Image: "app:2", Labels: h.c.labels(ss, first.ID+1)})
	if err != nil {
		t.Fatal(err)
	}

	rel := h.deploy(web, "app:3")
	if rel.Number != 2 || rel.Status != apitypes.ReleaseFailed || !strings.Contains(rel.Error, "docker rm -f "+orphan.ID[:min(len(orphan.ID), 12)]) {
		t.Fatalf("release = %d %s %q", rel.Number, rel.Status, rel.Error)
	}
	if running := h.rt.Running(); !slices.Contains(running, orphan.Name) || !slices.Contains(running, h.ctr(web, 1)) {
		t.Fatalf("running = %v, want the orphan and release 1 kept", running)
	}
	if svc, _ := h.c.GetService(h.ctx, testSystem, web); svc.ActiveRelease == nil || *svc.ActiveRelease != 1 {
		t.Fatalf("active release = %v", svc.ActiveRelease)
	}

	// Once it's removed, the next deploy goes ahead.
	if err := h.rt.Stop(h.ctx, orphan.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.Remove(h.ctx, orphan.ID, node.RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if r := h.deploy(web, "app:3"); r.Status != apitypes.ReleaseActive {
		t.Fatalf("after removing the orphan: %s %q", r.Status, r.Error)
	}
}
