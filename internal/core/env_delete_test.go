package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Deleting an environment archives its services, removes its containers,
// network and volumes and every row of it, frees its name and hostnames
// and leaves the project's other environments running. Only an admin of
// the whole org deletes, with the name typed again and a recent sign-in,
// and never the project's only environment.
func TestDeleteEnvironment(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "app:1")
	running, networks, volumes := h.rt.Running(), h.rt.Networks(), h.rt.Volumes()

	staging := EnvRef{"shop", "staging"}
	_, err := h.c.DeleteEnvironment(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.DeleteEnvironmentRequest{Confirm: "production"})
	wantCode(t, "the only environment", err, apitypes.CodeValidation)
	env, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	api := ServiceRef{"shop", "staging", "api"}
	if _, err := h.c.CreateService(h.ctx, testSystem, staging, apitypes.CreateServiceRequest{Name: "api", Port: 8080}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AddVolume(h.ctx, testSystem, api, apitypes.AddVolumeRequest{Name: "files", MountPath: "/files"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AddDomain(h.ctx, testSystem, api, apitypes.AddDomainRequest{Hostname: "staging.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.ApplyChanges(h.ctx, testSystem, staging); err != nil {
		t.Fatal(err)
	}
	h.deploy(api, "app:1")
	if len(h.rt.Volumes()) == len(volumes) {
		t.Fatal("the staging volume wasn't made")
	}
	confirm := apitypes.DeleteEnvironmentRequest{Confirm: "staging"}

	_, err = h.c.DeleteEnvironment(h.ctx, Actor{Kind: "token", OrgID: 1, Scope: apitypes.ScopeDeploy}, staging, confirm)
	wantCode(t, "a deploy token", err, apitypes.CodeForbidden)
	_, err = h.c.DeleteEnvironment(h.ctx, testSystem, staging, apitypes.DeleteEnvironmentRequest{Confirm: "stagin"})
	wantCode(t, "a mistyped name", err, apitypes.CodeValidation)
	_, err = h.c.DeleteEnvironment(h.ctx, testSystem, staging, apitypes.DeleteEnvironmentRequest{Confirm: "staging", ID: env.ID + 1})
	wantCode(t, "another environment's ID", err, apitypes.CodeConflict)
	// One delete per project at a time.
	h.c.envDeletes.Store(projectID(t, h), true)
	_, err = h.c.DeleteEnvironment(h.ctx, testSystem, staging, confirm)
	wantCode(t, "while another delete runs", err, apitypes.CodeConflict)
	h.c.envDeletes.Delete(projectID(t, h))
	stale := age(t, h, adaSession(t, h))
	_, err = h.c.DeleteEnvironment(h.ctx, stale.a, staging, confirm)
	wantCode(t, "a stale session", err, apitypes.CodeReauthRequired)

	got, err := h.c.DeleteEnvironment(h.ctx, testSystem, staging, confirm)
	if err != nil {
		t.Fatal(err)
	}
	if got.Environment != "staging" || got.Services != 1 || got.Volumes != 1 || got.Previews != 0 {
		t.Errorf("deleted = %+v", got)
	}
	h.waitRunning(running...)
	if v, n := h.rt.Volumes(), h.rt.Networks(); !slices.Equal(v, volumes) || !slices.Equal(n, networks) {
		t.Errorf("on the server: volumes %v, networks %v; want %v, %v", v, n, volumes, networks)
	}
	_, err = h.c.GetEnvironment(h.ctx, testSystem, staging)
	wantCode(t, "the deleted environment", err, apitypes.CodeNotFound)
	if _, err := h.c.GetService(h.ctx, testSystem, web); err != nil {
		t.Errorf("production's service: %v", err)
	}
	audit, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(audit, func(e apitypes.AuditEvent) bool {
		return e.Action == "environment.delete" && e.Target == "shop/staging"
	}) {
		t.Errorf("activity after delete: %+v", audit)
	}

	// The name and the hostname are free.
	again, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"})
	if err != nil || again.ID <= env.ID {
		t.Fatalf("recreate = %+v, %v (old ID %d)", again, err, env.ID)
	}
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "staging.example.com"}); err != nil {
		t.Errorf("the deleted environment's hostname: %v", err)
	}
}

// An environment with an open preview isn't deleted: the preview is
// closed first. Closed previews cloned from it go with it; a preview
// itself is closed, never deleted.
func TestDeleteEnvironmentWithPreviews(t *testing.T) {
	h, _, _, appHook := previewHarness(t)
	h.openPreview(appHook, 7)
	h.previewLive("pr-7")
	staging, pr := EnvRef{"shop", "staging"}, EnvRef{"shop", "pr-7"}
	_, err := h.c.DeleteEnvironment(h.ctx, testSystem, pr, apitypes.DeleteEnvironmentRequest{Confirm: "pr-7"})
	wantCode(t, "a preview", err, apitypes.CodeValidation)
	_, err = h.c.DeleteEnvironment(h.ctx, testSystem, staging, apitypes.DeleteEnvironmentRequest{Confirm: "staging"})
	wantCode(t, "with an open preview", err, apitypes.CodeConflict)

	id := h.envID("pr-7")
	if _, err := h.c.ClosePreview(h.ctx, testSystem, pr); err != nil {
		t.Fatal(err)
	}
	h.waitArchived(id, h.previewVolumes(id))
	h.settled("staging")
	got, err := h.c.DeleteEnvironment(h.ctx, testSystem, staging, apitypes.DeleteEnvironmentRequest{Confirm: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Services != 6 || got.Previews != 1 {
		t.Errorf("deleted = %+v", got)
	}
	if _, err := h.store.GetEnvironment(h.ctx, id); err == nil {
		t.Error("the closed preview's environment is still there")
	}
	if left := h.rt.Volumes(); slices.ContainsFunc(left, func(v string) bool { return strings.HasPrefix(v, "dock_p") }) {
		t.Errorf("volumes left: %v", left)
	}
}

// A service made after the delete read what it removes (deployed and
// archived again meanwhile) stops the delete's final step, which keeps
// every row: its container or volume would be left without them.
func TestDeleteEnvironmentRefusesWhatCameMeanwhile(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	staging := EnvRef{"shop", "staging"}
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	es, err := h.c.loadEnv(h.ctx, 1, staging)
	if err != nil {
		t.Fatal(err)
	}
	d, err := h.c.loadDeletion(h.ctx, es.project)
	if err != nil {
		t.Fatal(err)
	}
	d = d.of("environment shop/staging", []int64{es.env.ID})
	late := ServiceRef{"shop", "staging", "late"}
	if _, err := h.c.CreateService(h.ctx, testSystem, staging, apitypes.CreateServiceRequest{Name: "late"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.ArchiveService(h.ctx, testSystem, late); err != nil {
		t.Fatal(err)
	}
	wantCode(t, "a service made meanwhile", h.c.deleteEnvironmentRows(h.ctx, es, d), apitypes.CodeConflict)
	if _, err := h.c.GetEnvironment(h.ctx, testSystem, staging); err != nil {
		t.Errorf("the refused delete deleted: %v", err)
	}
}
