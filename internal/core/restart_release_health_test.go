package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// A release whose container is started again (ensureRunning after a lost
// container) reads the new container's health instead of keeping none.
func TestRestartedReleaseAdoptsHealth(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000, HealthCheckPath: "/healthz"})
	h.deploy(web, "nginx:1")
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	id := h.ctrID(web, 1)
	h.rt.SetHealth(id, node.HealthHealthy, "")
	h.until("healthy", func() bool { return h.healthOf(ss.svc.ID).Health == apitypes.HealthHealthy })

	active, err := h.store.GetActiveRelease(h.ctx, ss.svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.rt.Stop(h.ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.Remove(h.ctx, id, node.RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := h.c.ensureRunning(h.ctx, ss, active); err != nil {
		t.Fatal(err)
	}
	if got := h.healthOf(ss.svc.ID).Health; got != apitypes.HealthStarting {
		t.Fatalf("health after restart = %q, want starting", got)
	}
}
