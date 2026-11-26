package core

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A build that needs the registry while a prune's garbage collection has
// it stopped (an hour, in a large bucket) stops the collection instead of
// waiting for it. The stopped prune is no failure, and the next one
// collects.
func TestBuildStopsTheRegistrysGarbageCollection(t *testing.T) {
	p, _ := labPool(t)
	reg := &fakeRegistry{pruned: 1, gcHangs: true}
	p.withRegistry(reg)
	p.buildsProduction("homelab")
	web := p.createService("production", "web", apitypes.CreateServiceRequest{})
	p.deploySource(web) // its prune deletes a manifest and collects, for ever

	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(reg.Ops(), "gc") {
		if time.Now().After(deadline) {
			t.Fatalf("registry ops: %v", reg.Ops())
		}
		time.Sleep(2 * time.Millisecond)
	}
	r := p.deploySource(web)
	if r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 2: %s (%s)", r.Status, r.Error)
	}
	full, err := p.c.GetRelease(p.ctx, testSystem, web, r.Number)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(full.Events, func(e apitypes.ReleaseEvent) bool {
		return strings.HasPrefix(e.Message, "Waiting for the platform registry")
	}) || !slices.Contains(reg.Ops(), "gc stopped") {
		t.Fatalf("events %+v, registry ops %v", full.Events, reg.Ops())
	}
	u := &p.c.images
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.lastErr != "" || !u.collect || !u.lastPrune.IsZero() {
		t.Fatalf("after the stopped prune: err %q, collect %v, last %v", u.lastErr, u.collect, u.lastPrune)
	}
}

// A deploy waits for a registry that a migration or storage change holds
// only so long, then fails saying which step stalled, in its error and
// its diagnosis.
func TestDeployGivesUpOnABusyRegistry(t *testing.T) {
	p, _ := labPool(t)
	p.withRegistry(&fakeRegistry{})
	p.buildsProduction("homelab")
	web := p.createService("production", "web", apitypes.CreateServiceRequest{})
	release, err := p.c.holdRegistry()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	defer func(was time.Duration) { registryWait = was }(registryWait)
	registryWait = 50 * time.Millisecond

	r := p.deploySource(web)
	if r.Status != apitypes.ReleaseFailed || r.ErrorCode != apitypes.CodeBuildFailed ||
		!strings.HasPrefix(r.Error, "waiting for the platform registry didn't finish in 50ms") {
		t.Fatalf("release = %s %s %q", r.Status, r.ErrorCode, r.Error)
	}
	if d := r.Diagnosis; d == nil || d.Rule != "phase_timeout" || !strings.HasPrefix(d.Summary, "Waiting for the platform registry didn't finish") {
		t.Fatalf("diagnosis = %+v", r.Diagnosis)
	}
}
