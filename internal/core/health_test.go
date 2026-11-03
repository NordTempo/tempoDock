package core

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// ctrID is the fake runtime's container ID of a service's release.
func (h *harness) ctrID(ref ServiceRef, number int64) string {
	h.t.Helper()
	name := h.ctr(ref, number)
	cs, err := h.rt.List(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, ct := range cs {
		if ct.Name == name {
			return ct.ID
		}
	}
	h.t.Fatalf("no container %s", name)
	return ""
}

// until waits for cond, failing with what after 5 seconds.
func (h *harness) until(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatal("timed out waiting: " + what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// restarts counts the fake runtime's restarts of container name.
func (h *harness) restarts(name string) int {
	n := 0
	for _, op := range h.rt.Ops() {
		if op == "restart "+name {
			n++
		}
	}
	return n
}

// healthSettled waits until no unhealthy service is being handled.
func (h *harness) healthSettled() {
	h.t.Helper()
	h.until("health handling done", func() bool {
		h.c.health.mu.Lock()
		defer h.c.health.mu.Unlock()
		for _, s := range h.c.health.svc {
			if s.busy {
				return false
			}
		}
		return true
	})
}

// healthOf is a service's current health status.
func (h *harness) healthOf(id int64) apitypes.ServiceStatus { return h.c.getStatus(id) }

// A running service whose check fails is restarted at once and alerted
// on; failing again within the backoff only escalates the alert, the tick
// makes the restart once it is due, and passing again resolves the alert.
// Health restarts don't count as crashes.
func TestUnhealthyServiceIsRestartedWithBackoff(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000, HealthCheckPath: "/healthz"})
	h.deploy(web, "nginx:1")
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	id, name, key := h.ctrID(web, 1), h.ctr(web, 1), healthAlertKey(ss.svc.ID)
	if got := h.healthOf(ss.svc.ID).Health; got != apitypes.HealthStarting {
		t.Fatalf("health after deploy = %q, want starting", got)
	}
	h.rt.SetHealth(id, node.HealthHealthy, "")
	h.until("healthy", func() bool { return h.healthOf(ss.svc.ID).Health == apitypes.HealthHealthy })

	h.rt.SetHealth(id, node.HealthUnhealthy, "GET /healthz: timed out")
	h.until("first restart", func() bool { return h.restarts(name) == 1 })
	a := h.openAlert(key, true)
	if a.Kind != string(apitypes.AlertUnhealthy) || a.Severity != string(apitypes.SeverityWarning) || !strings.Contains(a.Message, "GET /healthz: timed out") {
		t.Fatalf("alert = %+v", a)
	}
	h.until("restart counted", func() bool {
		s := h.healthOf(ss.svc.ID)
		return s.HealthRestarts == 1 && s.Health == apitypes.HealthStarting
	})
	if s := h.healthOf(ss.svc.ID); s.State != apitypes.ServiceRunning || s.Health != apitypes.HealthStarting || s.RestartCount != 0 {
		t.Fatalf("status after restart = %+v", s)
	}

	// Failing again right away: the backoff holds the restart back.
	h.rt.SetHealth(id, node.HealthUnhealthy, "GET /healthz: timed out")
	h.until("critical", func() bool { return h.openAlert(key, true).Severity == string(apitypes.SeverityCritical) })
	h.c.healthTick(h.ctx, time.Now())
	h.healthSettled()
	if n := h.restarts(name); n != 1 {
		t.Fatalf("restarts within the backoff = %d, want 1", n)
	}
	h.c.healthTick(h.ctx, time.Now().Add(2*time.Minute))
	h.until("second restart", func() bool { return h.restarts(name) == 2 })

	h.rt.SetHealth(id, node.HealthHealthy, "")
	h.openAlert(key, false)
	h.until("healthy again", func() bool { return h.healthOf(ss.svc.ID).Health == apitypes.HealthHealthy })
	if _, open := h.c.alerts.open["crash:"+alertServiceKey(ss.svc.ID, "", "", "")]; open {
		t.Fatal("health restarts raised a crash loop alert")
	}
}

// A service with a deploy in flight isn't restarted: the deploy replaces
// its container (for a stateful service, after stopping this one).
func TestUnhealthyServiceDuringDeployIsLeftToTheDeploy(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	h.deploy(web, "nginx:1")
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	release := h.rt.HoldHealthChecks()
	defer release()
	if _, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "nginx:2"}); err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, 2, apitypes.ReleaseHealthChecking)

	h.rt.SetHealth(h.ctrID(web, 1), node.HealthUnhealthy, "connect to port 3000: connection refused")
	a := h.openAlert(healthAlertKey(ss.svc.ID), true)
	if !strings.Contains(a.Message, "deploy is in progress") {
		t.Fatalf("alert = %+v", a)
	}
	h.healthSettled()
	if n := h.restarts(h.ctr(web, 1)); n != 0 {
		t.Fatalf("restarted %d times during a deploy", n)
	}
	// The new release going live resolves it.
	release()
	if r := h.wait(web, 2); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 2 %s", r.Status)
	}
	h.openAlert(healthAlertKey(ss.svc.ID), false)
}

// A container that turned unhealthy while the daemon was down is handled
// on start; one that stopped is never started by a health restart.
func TestUnhealthyAfterDaemonRestart(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	api := h.service("api", apitypes.CreateServiceRequest{Port: 3001})
	h.deploy(web, "nginx:1")
	h.deploy(api, "nginx:1")
	webID, apiID := h.ctrID(web, 1), h.ctrID(api, 1)

	h.c.Close()
	h.rt.SetHealth(webID, node.HealthUnhealthy, "GET /: returned 500")
	h.rt.SetHealth(apiID, node.HealthUnhealthy, "GET /: returned 500")
	if err := h.rt.Stop(h.ctx, apiID, 0); err != nil {
		t.Fatal(err)
	}
	h.c = h.start()

	h.until("restart after start", func() bool { return h.restarts(h.ctr(web, 1)) == 1 })
	h.healthSettled()
	if n := h.restarts(h.ctr(api, 1)); n != 0 {
		t.Fatalf("a stopped container was restarted %d times", n)
	}
	if !slices.Contains(h.rt.Running(), h.ctr(web, 1)) || slices.Contains(h.rt.Running(), h.ctr(api, 1)) {
		t.Fatalf("running = %v", h.rt.Running())
	}
}
