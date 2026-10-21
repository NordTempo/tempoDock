package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Discarding a selection drops exactly those changes, all or nothing.
func TestDiscardSelectedChanges(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	env := web.Env()
	must := func(_ apitypes.ChangeResult, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(h.c.SetVariables(h.ctx, System, env, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"A": "1", "B": "2"}}))
	must(h.c.SetVariables(h.ctx, System, env, apitypes.SetVariablesRequest{Set: map[string]string{"S": "1"}}))
	port, memory := 4000, 256
	must(h.c.UpdateService(h.ctx, System, web, apitypes.UpdateServiceRequest{Port: &port, MemoryMB: &memory}))
	must(h.c.AddVolume(h.ctx, System, web, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}))

	p, err := h.c.DiscardChanges(h.ctx, System, env, apitypes.DiscardChangesRequest{
		Variables: []apitypes.VariableRef{{Key: "A", Service: "web"}, {Key: "S"}},
		Settings:  []apitypes.SettingRef{{Service: "web", Field: "port"}},
		Volumes:   []apitypes.VolumeRef{{Service: "web", Name: "data"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Count != 2 || p.Variables[0].Key != "B" || p.Settings[0].Field != "memory_mb" {
		t.Fatalf("left staged: %+v", p)
	}

	// A selection with anything not staged discards nothing.
	_, err = h.c.DiscardChanges(h.ctx, System, env, apitypes.DiscardChangesRequest{
		Variables: []apitypes.VariableRef{{Key: "B", Service: "web"}, {Key: "A", Service: "web"}},
	})
	if CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("discarding an unstaged change: %v", err)
	}
	if p, _ := h.c.GetChanges(h.ctx, System, env, false); p.Count != 2 {
		t.Fatalf("a failed discard changed something: %+v", p)
	}

	if p, err := h.c.DiscardChanges(h.ctx, System, env, apitypes.DiscardChangesRequest{}); err != nil || p.Count != 0 {
		t.Fatalf("discard all: %+v %v", p, err)
	}
}
