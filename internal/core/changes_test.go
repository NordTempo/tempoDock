package core

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
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

// Applying while the environment's server is offline is refused before
// anything goes live: the changes stay staged and no release exists.
func TestApplyOnOfflineServerKeepsChangesStaged(t *testing.T) {
	p, _ := labPool(t)
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	env := web.Env()
	if _, err := p.c.SetVariables(p.ctx, testSystem, env, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"MODE": "one"}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	p.deploy(web, "app:1")
	if _, err := p.c.SetVariables(p.ctx, testSystem, env, apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"MODE": "two"}}); err != nil {
		t.Fatal(err)
	}

	p.setOffline("homelab", true)
	if _, err := p.c.ApplyChanges(p.ctx, testSystem, env); CodeOf(err) != apitypes.CodeServerOffline {
		t.Fatalf("apply on an offline server: %v", err)
	}
	pending, err := p.c.GetChanges(p.ctx, testSystem, env, true)
	if err != nil || pending.Count != 1 || *pending.Variables[0].New != "two" {
		t.Fatalf("staged after the refused apply: %+v %v", pending, err)
	}
	if rels, _ := p.c.ListReleases(p.ctx, testSystem, web, 10); len(rels) != 1 {
		t.Fatalf("releases = %d, want only the first", len(rels))
	}
}

// A service that is deploying when changes are applied gets no release
// for them, so its own changes stay staged instead of going live unseen.
func TestApplySkipsDeployingServiceKeepsItsChangesStaged(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	api := h.service("api", apitypes.CreateServiceRequest{})
	env := web.Env()
	h.deploy(web, "app:1")
	h.deploy(api, "app:1")
	gate := make(chan struct{})
	h.rt.FailHealthCheck(func(node.ContainerSpec) error {
		<-gate
		return nil
	})
	rel, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:2"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, rel.Number, apitypes.ReleaseHealthChecking)
	h.set(env, "web", map[string]string{"A": "1"}, false)
	h.set(env, "api", map[string]string{"B": "1"}, false)

	applied, err := h.c.ApplyChanges(h.ctx, testSystem, env)
	close(gate)
	if err != nil || !slices.Equal(applied.Skipped, []string{"web"}) || len(applied.Releases) != 1 || applied.Releases[0].Service != "api" {
		t.Fatalf("applied = %+v, %v", applied, err)
	}
	h.wait(api, applied.Releases[0].Number)
	h.wait(web, rel.Number)
	p, _ := h.c.GetChanges(h.ctx, testSystem, env, false)
	if p.Count != 1 || p.Variables[0].Key != "A" || p.Variables[0].Service != "web" {
		t.Fatalf("staged after the apply: %+v", p)
	}
}

// A failed image or source deploy puts the image setting back, and the
// staged settings it took with it are staged again.
func TestFailedDeployRestoresSettings(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	memory := 256
	if _, err := h.c.UpdateService(h.ctx, testSystem, web, apitypes.UpdateServiceRequest{MemoryMB: &memory}); err != nil {
		t.Fatal(err)
	}

	h.rt.FailPull(errors.New("manifest unknown"))
	rel, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:typo"})
	if err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseFailed {
		t.Fatalf("release %d = %s", r.Number, r.Status)
	}
	svc, _ := h.c.GetService(h.ctx, testSystem, web)
	if svc.Image != "app:1" || svc.MemoryMB != 0 {
		t.Fatalf("live after the failed image deploy: image %q memory %d", svc.Image, svc.MemoryMB)
	}
	p, _ := h.c.GetChanges(h.ctx, testSystem, web.Env(), false)
	if p.Count != 1 || p.Settings[0].Field != "memory_mb" {
		t.Fatalf("staged after the failed image deploy: %+v", p)
	}

	h.rt.FailPull(nil)
	h.rt.FailBuild(errors.New("no Dockerfile"))
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: 11, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("FROM app:1\n"))
	_ = tw.Close()
	rel, err = h.c.DeploySource(h.ctx, testSystem, web, &buf, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseFailed {
		t.Fatalf("release %d = %s", r.Number, r.Status)
	}
	if svc, _ := h.c.GetService(h.ctx, testSystem, web); svc.Image != "app:1" || svc.MemoryMB != 0 {
		t.Fatalf("live after the failed source deploy: image %q memory %d", svc.Image, svc.MemoryMB)
	}
	if p, _ := h.c.GetChanges(h.ctx, testSystem, web.Env(), false); p.Count != 1 || p.Settings[0].Field != "memory_mb" {
		t.Fatalf("staged after the failed source deploy: %+v", p)
	}
}

// Staging the image a service already serves applies without a release:
// after a failed deploy (the image un-stages) and after a rollback (the
// live setting still names the newer image).
func TestApplyServedImageStartsNoRelease(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1", Port: 3000, HealthCheckPath: "/healthz"})
	h.deploy(web, "app:1")
	h.rt.FailHealthCheck(func(s node.ContainerSpec) error {
		if strings.HasSuffix(s.Name, "_r2") {
			return errors.New("GET /healthz: 500")
		}
		return nil
	})
	use := func(image string) []apitypes.Release {
		t.Helper()
		res, err := h.c.UpdateService(h.ctx, testSystem, web, apitypes.UpdateServiceRequest{Image: &image, Apply: true})
		if err != nil {
			t.Fatal(err)
		}
		if res.Applied == nil {
			return nil
		}
		for _, r := range res.Applied.Releases {
			h.wait(web, r.Number)
		}
		return res.Applied.Releases
	}
	if rels := use("app:2"); len(rels) != 1 {
		t.Fatalf("apply app:2: %+v", rels)
	}
	if rels := use("app:1"); len(rels) != 0 {
		t.Fatalf("app:1 after the failure released: %+v", rels)
	}

	if rels := use("app:3"); len(rels) != 1 { // r3, live
		t.Fatalf("apply app:3: %+v", rels)
	}
	rb, err := h.c.Rollback(h.ctx, testSystem, web, 1)
	if err != nil {
		t.Fatal(err)
	}
	h.wait(web, rb.Number)
	if rels := use("app:1"); len(rels) != 0 {
		t.Fatalf("app:1 after the rollback released: %+v", rels)
	}
	if p, _ := h.c.GetChanges(h.ctx, testSystem, web.Env(), false); p.Count != 0 {
		t.Fatalf("still staged: %+v", p)
	}
	if svc, _ := h.c.GetService(h.ctx, testSystem, web); svc.Image != "app:1" {
		t.Fatalf("live image %q", svc.Image)
	}
}

// Rolling back puts the service's own variables back to the release's, so
// the next unrelated apply doesn't bring the newer values back; shared
// variables keep their values and the rollback says it ran older ones.
func TestRollbackPutsVariablesBack(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"KEEP": "k"}}); err != nil {
		t.Fatal(err)
	}
	h.wait(web, h.deploy(web, "app:1").Number) // r1 takes KEEP live
	h.setVars(web, map[string]string{"VERSION": "v2", "KEEP": "k2"})
	shared := apitypes.SetVariablesRequest{Set: map[string]string{"REGION": "eu"}, Apply: true}
	res, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), shared)
	if err != nil {
		t.Fatal(err)
	}
	last := h.wait(web, res.Applied.Releases[0].Number)
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"STAGED": "s"}}); err != nil {
		t.Fatal(err)
	}

	rb, err := h.c.Rollback(h.ctx, testSystem, web, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, rb.Number); r.Status != apitypes.ReleaseActive || r.Number != last.Number+1 {
		t.Fatalf("rollback = %+v", r)
	}
	if got := h.revealed(web); got["KEEP"] != "k" || got["VERSION"] != "" {
		t.Errorf("live variables after the rollback = %v, want release 1's", got)
	}
	r, _ := h.c.GetRelease(h.ctx, testSystem, web, rb.Number)
	events := fmt.Sprint(r.Events)
	if !strings.Contains(events, "web's variables are release 1's again: KEEP, VERSION") || !strings.Contains(events, "older shared REGION") {
		t.Errorf("rollback events = %s", events)
	}
	if p, _ := h.c.GetChanges(h.ctx, testSystem, web.Env(), false); p.Count != 1 {
		t.Errorf("the staged change is gone or more were staged: %+v", p)
	}

	// The next apply ships the rolled-back values, the staged one and the
	// shared one's current value.
	applied, err := h.c.ApplyChanges(h.ctx, testSystem, web.Env())
	if err != nil {
		t.Fatal(err)
	}
	next := h.wait(web, applied.Releases[0].Number)
	spec, _ := h.rt.Spec(h.ctr(web, next.Number))
	if spec.Env["KEEP"] != "k" || spec.Env["VERSION"] != "" || spec.Env["STAGED"] != "s" || spec.Env["REGION"] != "eu" {
		t.Errorf("the apply after the rollback runs %v", spec.Env)
	}
}

// A deploy that takes a service's staged variable live redeploys the
// services referencing it, as an apply does, and its events name them.
func TestDeployRedeploysReferencingServices(t *testing.T) {
	h := newHarness(t)
	api := h.service("api", apitypes.CreateServiceRequest{Image: "app:1"})
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.wait(api, h.deploy(api, "app:1").Number)
	h.wait(web, h.deploy(web, "app:1").Number)
	h.setVars(api, map[string]string{"SECRET_KEY": "hunter3"})
	h.setVars(web, map[string]string{"API_SECRET": "${{api.SECRET_KEY}}"})
	webLive, _ := h.c.GetService(h.ctx, testSystem, web)

	if _, err := h.c.SetVariables(h.ctx, testSystem, api.Env(), apitypes.SetVariablesRequest{Service: "api", Set: map[string]string{"SECRET_KEY": "hunter4"}}); err != nil {
		t.Fatal(err)
	}
	rel := h.wait(api, h.deploy(api, "app:2").Number)
	next := h.wait(web, *webLive.ActiveRelease+1)
	if next.Status != apitypes.ReleaseActive || next.Source != apitypes.SourceConfig {
		t.Fatalf("web after api's deploy = %+v", next)
	}
	if spec, _ := h.rt.Spec(h.ctr(web, next.Number)); spec.Env["API_SECRET"] != "hunter4" {
		t.Errorf("web runs API_SECRET=%q, want api's new value", spec.Env["API_SECRET"])
	}
	r, _ := h.c.GetRelease(h.ctx, testSystem, api, rel.Number)
	if !strings.Contains(fmt.Sprint(r.Events), "Redeploying web (release") {
		t.Errorf("api's release events don't name web: %v", r.Events)
	}
	if p, _ := h.c.GetChanges(h.ctx, testSystem, api.Env(), false); p.Count != 0 {
		t.Errorf("staged after the deploy: %+v", p)
	}
}

// Staged changes name the services applying would redeploy: running ones,
// not those that take the change with their first deploy.
func TestChangesNameRedeploys(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.wait(web, h.deploy(web, "app:1").Number)
	h.service("api", apitypes.CreateServiceRequest{Image: "app:1"}) // never deployed
	shared := apitypes.SetVariablesRequest{Set: map[string]string{"REGION": "eu"}}
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), shared); err != nil {
		t.Fatal(err)
	}
	p, err := h.c.GetChanges(h.ctx, testSystem, web.Env(), false)
	if err != nil || !slices.Equal(p.Affected, []string{"api", "web"}) || !slices.Equal(p.Redeploys, []string{"web"}) {
		t.Fatalf("affected %v, redeploys %v, %v", p.Affected, p.Redeploys, err)
	}
}

// Unsetting a variable that isn't there says so instead of staging
// nothing; a shared one unset on a service names --shared.
func TestUnsetUnknownVariable(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Set: map[string]string{"LOG_LEVEL": "debug"}}); err != nil {
		t.Fatal(err)
	}
	_, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Unset: []string{"LOG_LEVEL"}})
	if CodeOf(err) != apitypes.CodeNotFound || !strings.Contains(err.Error(), "--shared") {
		t.Errorf("unset a shared variable on web: %v", err)
	}
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Unset: []string{"NOPE"}}); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("unset NOPE: %v", err)
	}
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Unset: []string{"LOG_LEVEL"}}); err != nil {
		t.Errorf("unset the shared one: %v", err)
	}
}
