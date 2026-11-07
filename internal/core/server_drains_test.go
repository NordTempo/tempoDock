package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// waitDrain waits for server's drain to stop running.
func (p *pool) waitDrain(server string) apitypes.ServerDrain {
	p.t.Helper()
	var d apitypes.ServerDrain
	eventually(p.t, "the drain to stop running", func() bool {
		var err error
		if d, err = p.c.ServerDrainStatus(p.ctx, testSystem, server); err != nil {
			p.t.Fatal(err)
		}
		return d.Status != apitypes.DrainRunning
	})
	return d
}

// TestDrainServer plans a drain, runs it, and removes the drained server:
// the environment and its data are on the other server, and the old one
// goes with its history.
func TestDrainServer(t *testing.T) {
	p, _, db, _, data := movePool(t)
	admin := p.token(testSystem, apitypes.CreateTokenRequest{Name: "admin", Scope: apitypes.ScopeAdmin})
	if _, err := p.c.DrainServer(p.ctx, admin, "homelab", apitypes.DrainServerRequest{DryRun: true}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("a non-operator drained: %v", err)
	}
	plan, err := p.c.DrainServer(p.ctx, testSystem, "homelab", apitypes.DrainServerRequest{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 1 || plan.Items[0].To != p.ids2name("cp") || !plan.Items[0].Stateful || plan.Items[0].DataBytes != 4096 || len(plan.Problems) != 0 || plan.ID != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if p.server("shop", "staging") != "homelab" {
		t.Fatal("a dry run moved something")
	}

	if _, err := p.c.DrainServer(p.ctx, testSystem, "homelab", apitypes.DrainServerRequest{}); err != nil {
		t.Fatal(err)
	}
	d := p.waitDrain("homelab")
	if d.Status != apitypes.DrainSucceeded || d.Items[0].Status != apitypes.DrainMoved || len(d.Forwards) != 1 || d.Forwards[0].Service != "web" {
		t.Fatalf("drain = %+v", d)
	}
	if p.server("shop", "staging") != p.ids2name("cp") || string(p.rt.VolumeData(p.volume(db, "data"))) != string(data) {
		t.Fatalf("staging is on %s with %q", p.server("shop", "staging"), p.rt.VolumeData(p.volume(db, "data")))
	}
	if srv, err := p.store.GetServer(p.ctx, p.ids["homelab"]); err != nil || !srv.DisabledForPlacement {
		t.Errorf("homelab still takes placements: %+v, %v", srv, err)
	}
	if err := p.c.RemoveServer(p.ctx, testSystem, "homelab"); err != nil {
		t.Fatalf("remove the drained server: %v", err)
	}
}

// TestDrainServerPausesAndSkips: an environment that can't move stays
// where it was and pauses the drain; resuming with skip leaves it there
// and finishes. A kind drain stops the server taking that kind.
func TestDrainServerPausesAndSkips(t *testing.T) {
	p, lab, db, _, data := movePool(t)
	p.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	if _, err := p.c.DrainServer(p.ctx, testSystem, "homelab", apitypes.DrainServerRequest{Kind: apitypes.KindNonProduction}); err != nil {
		t.Fatal(err)
	}
	d := p.waitDrain("homelab")
	if d.Status != apitypes.DrainPaused || d.Items[0].Status != apitypes.DrainFailed || d.Items[0].Move == nil || !strings.Contains(d.Error, "stays on its server") {
		t.Fatalf("drain = %+v", d)
	}
	assertStayed(t, p, lab, db, data)
	if srv, err := p.store.GetServer(p.ctx, p.ids["homelab"]); err != nil || srv.AcceptsNonProduction {
		t.Errorf("homelab still accepts non-production: %+v, %v", srv, err)
	}
	if _, err := p.c.ResumeServerDrain(p.ctx, testSystem, "homelab", apitypes.ResumeDrainRequest{Skip: true}); err != nil {
		t.Fatal(err)
	}
	if d = p.waitDrain("homelab"); d.Status != apitypes.DrainSucceeded || d.Items[0].Status != apitypes.DrainSkipped {
		t.Fatalf("drain = %+v", d)
	}
	if err := p.c.RemoveServer(p.ctx, testSystem, "homelab"); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("removed a server that still has an environment: %v", err)
	}
}
