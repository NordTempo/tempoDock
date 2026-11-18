package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Deleting an archived project removes its containers, networks, volumes
// and snapshot files and every row of it, frees its name and hostnames,
// ends the tokens limited to it and keeps its activity. Only an admin of
// the whole org deletes, with the name typed again and a recent sign-in.
func TestDeleteArchivedProject(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	db := h.volumeService("db")
	h.snapshot(db)
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.ApplyChanges(h.ctx, testSystem, web.Env()); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:1")
	before, _ := h.c.GetProject(h.ctx, testSystem, "shop")
	limited, err := h.c.CreateToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "shop-admin", Scope: apitypes.ScopeAdmin, Project: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	limitedActor, err := h.c.AuthenticateToken(h.ctx, limited.Secret, "")
	if err != nil {
		t.Fatal(err)
	}
	confirm := apitypes.DeleteProjectRequest{Confirm: "shop"}

	_, err = h.c.DeleteProject(h.ctx, testSystem, "shop", confirm)
	wantCode(t, "a live project", err, apitypes.CodeValidation)
	if _, err := h.c.ArchiveProject(h.ctx, testSystem, "shop"); err != nil {
		t.Fatal(err)
	}
	h.waitRunning()

	for _, a := range []Actor{
		{Kind: "token", OrgID: 1, Scope: apitypes.ScopeDeploy},
		limitedActor,
	} {
		_, err := h.c.DeleteProject(h.ctx, a, "shop", confirm)
		wantCode(t, "actor "+a.Name, err, apitypes.CodeForbidden)
	}
	_, err = h.c.DeleteProject(h.ctx, testSystem, "shop", apitypes.DeleteProjectRequest{Confirm: "sho"})
	wantCode(t, "a mistyped name", err, apitypes.CodeValidation)
	_, err = h.c.DeleteProject(h.ctx, testSystem, "shop", apitypes.DeleteProjectRequest{Confirm: "shop", ID: before.ID + 1})
	wantCode(t, "another project's ID", err, apitypes.CodeConflict)
	stale := age(t, h, adaSession(t, h))
	_, err = h.c.DeleteProject(h.ctx, stale.a, "shop", confirm)
	wantCode(t, "a stale session", err, apitypes.CodeReauthRequired)
	// The project's volumes, named for it; the server's snapshot store
	// (dock_snapshots_*) is the server's.
	projectVolumes := func() []string {
		return slices.DeleteFunc(h.rt.Volumes(), func(v string) bool { return !strings.HasPrefix(v, "dock_p") })
	}
	if len(projectVolumes()) == 0 {
		t.Fatal("a refused delete removed volumes")
	}

	got, err := h.c.DeleteProject(h.ctx, testSystem, "shop", confirm)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != "shop" || got.Environments != 1 || got.Services != 2 || got.Volumes != 1 {
		t.Errorf("deleted = %+v", got)
	}
	if v, n := projectVolumes(), h.rt.Networks(); len(v) != 0 || len(n) != 0 {
		t.Errorf("left on the server: volumes %v, networks %v", v, n)
	}
	if !slices.ContainsFunc(h.rt.Ops(), func(op string) bool { return strings.HasPrefix(op, "job dock_snapshot_delete_") }) {
		t.Errorf("the snapshot's files weren't deleted: %v", h.rt.Ops())
	}
	_, err = h.c.GetProject(h.ctx, testSystem, "shop")
	wantCode(t, "the deleted project", err, apitypes.CodeNotFound)
	if ps, _ := h.c.ListProjects(h.ctx, testSystem, true); len(ps) != 0 {
		t.Errorf("listed after delete: %+v", ps)
	}
	if _, err := h.c.AuthenticateToken(h.ctx, limited.Secret, ""); err == nil {
		t.Error("a token limited to the deleted project still works")
	}
	audit, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(audit, func(e apitypes.AuditEvent) bool { return e.Action == "project.delete" && e.Target == "shop" }) ||
		!slices.ContainsFunc(audit, func(e apitypes.AuditEvent) bool { return e.Action == "project.create" && e.Target == "shop" }) {
		t.Errorf("activity after delete: %+v", audit)
	}

	// The name and the hostname are free; the new project is a new one.
	again, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "shop"})
	if err != nil || again.ID <= before.ID {
		t.Fatalf("recreate = %+v, %v (old ID %d)", again, err, before.ID)
	}
	h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Errorf("the deleted project's hostname: %v", err)
	}
}

// An archived project can be renamed, which frees its name.
func TestRenameArchivedProject(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.ArchiveProject(h.ctx, testSystem, "shop"); err != nil {
		t.Fatal(err)
	}
	p, err := h.c.RenameProject(h.ctx, testSystem, "shop", apitypes.RenameProjectRequest{Name: "shop-2025"})
	if err != nil || p.Name != "shop-2025" || p.ArchivedAt == nil {
		t.Fatalf("rename = %+v, %v", p, err)
	}
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		t.Fatalf("the freed name: %v", err)
	}
}

// Deleting waits for every server the project has something on: an
// offline one would keep its volumes, unknown to Dock.
func TestDeleteProjectNeedsItsServers(t *testing.T) {
	p, lab := labPool(t)
	web := p.createService("staging", "web", apitypes.CreateServiceRequest{})
	p.deploy(web, "app:1")
	if _, err := p.c.ArchiveProject(p.ctx, testSystem, "shop"); err != nil {
		t.Fatal(err)
	}
	waitOps(t, lab, "remove network")
	p.setOffline("homelab", true)
	_, err := p.c.DeleteProject(p.ctx, testSystem, "shop", apitypes.DeleteProjectRequest{Confirm: "shop"})
	wantCode(t, "with homelab offline", err, apitypes.CodeConflict)
	if !strings.Contains(err.Error(), "homelab (offline)") {
		t.Errorf("error = %v", err)
	}
	if _, err := p.c.GetProject(p.ctx, testSystem, "shop"); err != nil {
		t.Fatalf("the refused delete deleted: %v", err)
	}
	p.setOffline("homelab", false)
	if _, err := p.c.DeleteProject(p.ctx, testSystem, "shop", apitypes.DeleteProjectRequest{Confirm: "shop"}); err != nil {
		t.Fatal(err)
	}
}

// A token can't delete a project with a locked environment: like any
// change there, it becomes an access request (ADR 0044).
func TestDeleteProjectLockedForTokens(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.LockEnvironment(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.LockEnvironmentRequest{Reason: "prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.ArchiveProject(h.ctx, testSystem, "shop"); err != nil {
		t.Fatal(err)
	}
	admin := Actor{Name: "ci", Kind: "token", OrgID: 1, Scope: apitypes.ScopeAdmin}
	_, err := h.c.DeleteProject(h.ctx, admin, "shop", apitypes.DeleteProjectRequest{Confirm: "shop"})
	wantCode(t, "a token in a locked environment", err, apitypes.CodeEnvironmentLocked)
}

// The notes say where a deleted project's backups stay, per repository,
// with the restic tags that find them.
func TestKeptBackupNotes(t *testing.T) {
	notes := keptBackupNotes([]store.ListProjectKeptBackupsRow{
		{ServiceID: 4, Repository: "", Snapshots: 2},
		{ServiceID: 7, Repository: "", Snapshots: 1},
		{ServiceID: 4, Repository: "s3:https://s3.example.com/backups", Snapshots: 1},
	})
	if len(notes) != 2 {
		t.Fatalf("notes = %q", notes)
	}
	if !strings.HasPrefix(notes[0], "3 backups stay in the local repository on the service's server, tagged dock-s4, dock-s7 (manual ones with -manual added):") ||
		!strings.Contains(notes[0], "`restic snapshots --tag dock-s4 --tag dock-s4-manual --tag dock-s7 --tag dock-s7-manual`") {
		t.Errorf("local note = %q", notes[0])
	}
	if !strings.HasPrefix(notes[1], "1 backup stays in s3:https://s3.example.com/backups, tagged dock-s4 (manual ones with -manual added):") {
		t.Errorf("s3 note = %q", notes[1])
	}
}
