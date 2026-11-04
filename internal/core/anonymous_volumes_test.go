package core

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// A container that served is removed keeping its anonymous volumes, which
// may hold the service's data, and one that never went live is not (ADR
// 0052). The deploy says the image keeps data where no volume covers.
func TestServedContainersKeepAnonymousVolumes(t *testing.T) {
	h := newHarness(t)
	db := h.service("db", apitypes.CreateServiceRequest{})
	h.rt.SetImageVolumes("postgres:17", "/var/lib/postgresql/data")

	h.deploy(db, "postgres:17")
	r1, err := h.c.GetRelease(h.ctx, testSystem, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(r1.Events, func(e apitypes.ReleaseEvent) bool {
		return strings.HasPrefix(e.Message, "Warning: no volume covers /var/lib/postgresql/data,")
	}) {
		t.Fatalf("no warning about the uncovered VOLUME: %+v", r1.Events)
	}

	// Replaced: it served, so its data stays.
	h.deploy(db, "postgres:17")
	waitOps(t, h.rt, "remove "+h.ctr(db, 1))

	// Never went live: nothing of it is kept.
	h.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	if r := h.deploy(db, "postgres:17"); r.Status != apitypes.ReleaseFailed {
		t.Fatalf("release 3 = %s, want failed", r.Status)
	}
	h.rt.FailHealthCheck(nil)
	waitOps(t, h.rt, "remove "+h.ctr(db, 3))

	// Stopped: the active release's data stays too.
	if _, err := h.c.StopService(h.ctx, testSystem, db); err != nil {
		t.Fatal(err)
	}
	if got, want := h.rt.KeptAnonymousVolumes(), []string{h.ctr(db, 1), h.ctr(db, 2)}; !slices.Equal(got, want) {
		t.Fatalf("kept the anonymous volumes of %q, want %q", got, want)
	}
}
