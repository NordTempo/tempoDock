package core

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

func (h *harness) set(env EnvRef, service string, vars map[string]string, apply bool) apitypes.ChangeResult {
	h.t.Helper()
	res, err := h.c.SetVariables(h.ctx, testSystem, env, apitypes.SetVariablesRequest{Service: service, Set: vars, Apply: apply})
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

// Changing a variable other services reference redeploys them too, however
// indirectly, and the staged view lists them as affected.
func TestApplyRedeploysDependents(t *testing.T) {
	h := newHarness(t)
	db := h.service("postgres", apitypes.CreateServiceRequest{})
	api := h.service("api", apitypes.CreateServiceRequest{})
	worker := h.service("worker", apitypes.CreateServiceRequest{})
	other := h.service("other", apitypes.CreateServiceRequest{})
	env := db.Env()
	h.set(env, "postgres", map[string]string{"DATABASE_URL": "postgres://one"}, false)
	h.set(env, "api", map[string]string{"DB": "${{postgres.DATABASE_URL}}"}, false)
	h.set(env, "worker", map[string]string{"UPSTREAM": "${{api.DB}}"}, true)
	for _, ref := range []ServiceRef{db, api, worker, other} {
		h.deploy(ref, "app:1")
	}

	res := h.set(env, "postgres", map[string]string{"DATABASE_URL": "postgres://two"}, false)
	if got := res.Pending.Affected; !slices.Equal(got, []string{"api", "postgres", "worker"}) {
		t.Fatalf("affected = %v", got)
	}
	applied, err := h.c.ApplyChanges(h.ctx, testSystem, env)
	if err != nil {
		t.Fatal(err)
	}
	var started []string
	for _, r := range applied.Releases {
		started = append(started, r.Service)
		h.wait(ServiceRef{"shop", "production", r.Service}, r.Number)
	}
	slices.Sort(started)
	if !slices.Equal(started, []string{"api", "postgres", "worker"}) {
		t.Fatalf("releases for %v", started)
	}
	for ref, key := range map[ServiceRef]string{api: "DB", worker: "UPSTREAM"} {
		spec, _ := h.rt.Spec(h.ctr(ref, 2))
		if spec.Env[key] != "postgres://two" {
			t.Errorf("%s %s = %q", ref.Service, key, spec.Env[key])
		}
	}
}

// A failed config release doesn't leave its changes live: they are staged
// again (with the reason), the live values are what the active release
// runs with, and the next deploy doesn't ship them.
func TestFailedReleaseRestagesItsChanges(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	env := web.Env()
	h.set(env, "web", map[string]string{"MODE": "good", "KEEP": "1"}, false)
	h.deploy(web, "app:1")

	h.rt.FailHealthCheck(func(s node.ContainerSpec) error {
		if s.Env["MODE"] == "bad" {
			return errors.New("crashed: bad MODE")
		}
		return nil
	})
	memory := 256
	if _, err := h.c.UpdateService(h.ctx, testSystem, web, apitypes.UpdateServiceRequest{MemoryMB: &memory}); err != nil {
		t.Fatal(err)
	}
	res, err := h.c.SetVariables(h.ctx, testSystem, env, apitypes.SetVariablesRequest{
		Service: "web", Set: map[string]string{"MODE": "bad", "NEW": "x"}, Unset: []string{"KEEP"}, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, res.Applied.Releases[0].Number); r.Status != apitypes.ReleaseFailed {
		t.Fatalf("release 2 = %s", r.Status)
	}

	p, _ := h.c.GetChanges(h.ctx, testSystem, env, true)
	if p.Count != 4 || !strings.Contains(p.LastFailure, "release 2 of web failed") {
		t.Fatalf("staged again: %+v", p)
	}
	byKey := map[string]apitypes.VariableChange{}
	for _, v := range p.Variables {
		byKey[v.Key] = v
	}
	if v := byKey["MODE"]; v.Action != apitypes.ChangeUpdate || *v.Old != "good" || *v.New != "bad" {
		t.Errorf("MODE = %+v", v)
	}
	if v := byKey["NEW"]; v.Action != apitypes.ChangeAdd {
		t.Errorf("NEW = %+v", v)
	}
	if v := byKey["KEEP"]; v.Action != apitypes.ChangeRemove || *v.Old != "1" {
		t.Errorf("KEEP = %+v", v)
	}
	if svc, _ := h.c.GetService(h.ctx, testSystem, web); svc.MemoryMB != 0 {
		t.Errorf("live memory = %d, want the active release's (default)", svc.MemoryMB)
	}

	// Once discarded, the next deploy ships the active release's values.
	// (A deploy takes the service's own staged changes with it.)
	if _, err := h.c.DiscardChanges(h.ctx, testSystem, env, apitypes.DiscardChangesRequest{}); err != nil {
		t.Fatal(err)
	}
	r3 := h.deploy(web, "app:2")
	spec, _ := h.rt.Spec(h.ctr(web, r3.Number))
	if r3.Status != apitypes.ReleaseActive || spec.Env["MODE"] != "good" || spec.Env["KEEP"] != "1" || spec.Env["NEW"] != "" {
		t.Fatalf("release 3 %s env %v", r3.Status, spec.Env)
	}
	if p, _ := h.c.GetChanges(h.ctx, testSystem, env, false); p.Count != 0 || p.LastFailure != "" {
		t.Fatalf("after discard: %+v", p)
	}
}

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
	must(h.c.SetVariables(h.ctx, testSystem, env, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"A": "1", "B": "2"}}))
	must(h.c.SetVariables(h.ctx, testSystem, env, apitypes.SetVariablesRequest{Set: map[string]string{"S": "1"}}))
	port, memory := 4000, 256
	must(h.c.UpdateService(h.ctx, testSystem, web, apitypes.UpdateServiceRequest{Port: &port, MemoryMB: &memory}))
	must(h.c.AddVolume(h.ctx, testSystem, web, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}))

	p, err := h.c.DiscardChanges(h.ctx, testSystem, env, apitypes.DiscardChangesRequest{
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
	_, err = h.c.DiscardChanges(h.ctx, testSystem, env, apitypes.DiscardChangesRequest{
		Variables: []apitypes.VariableRef{{Key: "B", Service: "web"}, {Key: "A", Service: "web"}},
	})
	if CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("discarding an unstaged change: %v", err)
	}
	if p, _ := h.c.GetChanges(h.ctx, testSystem, env, false); p.Count != 2 {
		t.Fatalf("a failed discard changed something: %+v", p)
	}

	if p, err := h.c.DiscardChanges(h.ctx, testSystem, env, apitypes.DiscardChangesRequest{}); err != nil || p.Count != 0 {
		t.Fatalf("discard all: %+v %v", p, err)
	}
}
