package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// A rename preserves runtime resources, tokens and pre-rename service history.
func TestRenameProjectPreservesIdentityAndHistory(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	before, _ := h.c.GetProject(h.ctx, testSystem, "shop")
	token, err := h.c.CreateToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "scoped", Scope: apitypes.ScopeAdmin, Project: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	actor, _ := h.c.AuthenticateToken(h.ctx, token.Secret, "")
	ops := h.rt.Ops()
	oldAudit, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Project: "shop", Environment: "production", Service: "web"})
	got, err := h.c.RenameProject(h.ctx, actor, "shop", apitypes.RenameProjectRequest{ID: before.ID, Name: "storefront"})
	if err != nil || got.ID != before.ID || got.Name != "storefront" || !got.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("rename = %+v, %v", got, err)
	}
	if !reflect.DeepEqual(ops, h.rt.Ops()) {
		t.Fatalf("rename touched runtime: %v -> %v", ops, h.rt.Ops())
	}
	if _, err := h.c.GetProject(h.ctx, testSystem, "shop"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("old name: %v", err)
	}
	byID, err := h.c.GetProjectByID(h.ctx, actor, before.ID)
	if err != nil || byID.Name != got.Name {
		t.Fatalf("stable ID = %+v, %v", byID, err)
	}
	web.Project = got.Name
	if _, err := h.c.GetService(h.ctx, actor, web); err != nil {
		t.Fatal(err)
	}
	audit, err := h.c.ListAudit(h.ctx, actor, AuditFilter{Project: got.Name, Environment: "production", Service: "web"})
	if err != nil || len(audit) != len(oldAudit) || len(audit) == 0 {
		t.Fatalf("service history = %d -> %d, %v", len(oldAudit), len(audit), err)
	}
	if !strings.HasPrefix(audit[len(audit)-1].Target, "shop/") {
		t.Fatal("historical audit target was rewritten")
	}
	authenticated, err := h.c.AuthenticateToken(h.ctx, token.Secret, "")
	if err != nil || authenticated.ProjectID != before.ID || authenticated.ProjectName != got.Name {
		t.Fatalf("token = %+v, %v", authenticated, err)
	}
	if _, err := h.c.RenameProject(h.ctx, authenticated, got.Name, apitypes.RenameProjectRequest{Name: got.Name}); err != nil {
		t.Fatal(err)
	}
	renames, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Project: got.Name, Action: "project.rename"})
	if len(renames) != 1 || !strings.Contains(renames[0].Summary, "shop to storefront") {
		t.Fatalf("rename audit: %+v", renames)
	}
	h.restart()
	if p, err := h.c.GetProjectByID(h.ctx, testSystem, before.ID); err != nil || p.Name != got.Name {
		t.Fatalf("after restart: %+v, %v", p, err)
	}
}

// Creation and rename share name validation and permissions; archived names
// remain reserved and an expected ID prevents accidentally renaming a reused name.
func TestRenameProjectValidationAndScope(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	p, _ := h.c.GetProject(h.ctx, testSystem, "shop")
	e, _ := h.c.GetEnvironment(h.ctx, testSystem, EnvRef{"shop", "production"})
	other, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateProject(h.ctx, System.InOrg(other.ID), apitypes.CreateProjectRequest{Name: "storefront"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "Upper", "has space", "-bad", "bad-", strings.Repeat("a", 33), "audit", "settings", "tokens"} {
		if _, err := h.c.RenameProject(h.ctx, testSystem, "shop", apitypes.RenameProjectRequest{Name: name}); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("name %q: %v", name, err)
		}
	}
	for _, actor := range []Actor{
		{Kind: "token", OrgID: 1, Scope: apitypes.ScopeRead},
		{Kind: "token", OrgID: 1, Scope: apitypes.ScopeDeploy},
		{Kind: "token", OrgID: 1, Scope: apitypes.ScopeAdmin, ProjectID: p.ID, EnvironmentID: e.ID},
		{Kind: "token", OrgID: 1, Scope: apitypes.ScopeAdmin, ProjectID: p.ID + 99},
	} {
		if _, err := h.c.RenameProject(h.ctx, actor, "shop", apitypes.RenameProjectRequest{Name: "storefront"}); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("actor %+v: %v", actor, err)
		}
	}
	if _, err := h.c.GetProjectByID(h.ctx, System.InOrg(other.ID), p.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("cross-org ID: %v", err)
	}
	if _, err := h.c.RenameProject(h.ctx, testSystem, "shop", apitypes.RenameProjectRequest{ID: p.ID + 1, Name: "storefront"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("wrong identity: %v", err)
	}
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "taken"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.ArchiveProject(h.ctx, testSystem, "taken"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.RenameProject(h.ctx, testSystem, "shop", apitypes.RenameProjectRequest{Name: "taken"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("archived name: %v", err)
	}
	if _, err := h.c.RenameProject(h.ctx, testSystem, "taken", apitypes.RenameProjectRequest{Name: "new"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("archived rename: %v", err)
	}
	if _, err := h.c.RenameProject(h.ctx, testSystem, "shop", apitypes.RenameProjectRequest{Name: "storefront"}); err != nil {
		t.Fatalf("other org's name: %v", err)
	}
}

// In-flight deploys and their already-open, name-filtered event streams follow
// the stable identity. A stale scope must not publish the old project name again.
func TestRenameProjectDuringDeployment(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	p, _ := h.c.GetProject(h.ctx, testSystem, "shop")
	e, _ := h.c.GetEnvironment(h.ctx, testSystem, web.Env())
	reader := Actor{Kind: "token", OrgID: 1, Scope: apitypes.ScopeRead, ProjectID: p.ID, EnvironmentID: e.ID}
	stream, err := h.c.Events(h.ctx, reader, EventFilter(web))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	ready, events := make(chan struct{}), make(chan LiveEvent, 64)
	go func() {
		_ = stream(ctx, func() { close(ready) }, func(batch ...LiveEvent) error {
			for _, ev := range batch {
				select {
				case events <- ev:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}()
	<-ready
	release := h.rt.HoldHealthChecks()
	defer release()
	r, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:1"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, r.Number, apitypes.ReleaseHealthChecking)
	p, err = h.c.RenameProject(h.ctx, testSystem, "shop", apitypes.RenameProjectRequest{Name: "storefront"})
	if err != nil {
		t.Fatal(err)
	}
	release()
	web.Project = p.Name
	if r := h.wait(web, r.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release: %+v", r)
	}
	sawRename := false
	for {
		select {
		case ev := <-events:
			if ev.Type == apitypes.EventProjectRenamed {
				sawRename = true
			}
			if sawRename && (ev.Project == "shop" || (ev.Release != nil && ev.Release.Project == "shop")) {
				t.Fatalf("stale event after rename: %+v", ev.Event)
			}
			if ev.Release != nil && ev.Release.Status == apitypes.ReleaseActive {
				if !sawRename {
					t.Fatal("filtered stream missed rename")
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("filtered stream missed deployment completion")
		}
	}
}

// Alerts keep their identity across renames, including a stale queued failure;
// identical project names in another organization must not be renamed with them.
func TestRenameProjectPreservesOpenAlerts(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.rt.FailPull(errors.New("missing image"))
	h.deploy(web, "app:bad")
	before := h.openAlert("deploy:service:1", true)
	ss, _ := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	org, err := h.store.CreateOrg(h.ctx, store.CreateOrgParams{Slug: "other", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	other := System.InOrg(org.ID)
	project, err := h.c.CreateProject(h.ctx, other, apitypes.CreateProjectRequest{Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := h.c.CreateService(h.ctx, other, web.Env(), apitypes.CreateServiceRequest{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	signal := deployFailed("shop", "production", "web", 1, "other failure", service.ID)
	signal.projectID = project.ID
	h.c.raiseAlert(h.ctx, signal, time.Now())
	otherBefore := h.openAlert(signal.Key, true)
	if _, err := h.c.RenameProject(h.ctx, testSystem, "shop", apitypes.RenameProjectRequest{Name: "storefront"}); err != nil {
		t.Fatal(err)
	}
	stale := deployFailed("shop", "production", "web", 1, "missing image", ss.svc.ID)
	stale.projectID = ss.project.ID
	h.c.raiseAlert(h.ctx, stale, time.Now())
	after := h.openAlert("deploy:service:1", true)
	otherAfter := h.openAlert(signal.Key, true)
	if otherAfter.ID != otherBefore.ID || otherAfter.Project != "shop" || otherAfter.Title != otherBefore.Title {
		t.Fatalf("other org alert changed: %+v", otherAfter)
	}
	if after.ID != before.ID || after.Project != "storefront" || !strings.Contains(after.Title, "storefront/") {
		t.Fatalf("alert after rename: %+v", after)
	}
	h.restart()
	after = h.openAlert("deploy:service:1", true)
	if after.ID != before.ID {
		t.Fatalf("restart duplicated alert: %+v", after)
	}
	h.rt.FailPull(nil)
	web.Project = "storefront"
	h.deploy(web, "app:good")
	h.openAlert("deploy:service:1", false)
}

// Alert updates queued before a rename must not revert the live label.
// Closed occurrences remain snapshots, not current-name views.
func TestRenameProjectQueuedAlertEvent(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{})
	ss, _ := h.c.serviceFor(h.ctx, testSystem, ServiceRef{"shop", "production", "web"}, apitypes.ScopeRead, "testing")
	signal := deployFailed("shop", "production", "web", 1, "failure", ss.svc.ID)
	signal.projectID = ss.project.ID
	h.c.raiseAlert(h.ctx, signal, time.Now())
	row, err := h.store.GetAlert(h.ctx, h.openAlert(signal.Key, true).ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.RenameProject(h.ctx, testSystem, "shop", apitypes.RenameProjectRequest{Name: "storefront"}); err != nil {
		t.Fatal(err)
	}
	sub := h.c.bus.subscribe(func(e busEvent) bool { return e.ev.Type == apitypes.EventAlert })
	defer sub.close()
	h.c.publishAlert(row)
	event := <-sub.ch
	if event.Project != "storefront" || event.Alert.Project != "storefront" || !strings.Contains(event.Alert.Title, "storefront/") {
		t.Fatalf("stale alert: %+v", event.Event)
	}
	closed := time.Now()
	row.ResolvedAt = &closed
	h.c.publishAlert(row)
	event = <-sub.ch
	if event.Project != "shop" || event.Alert.Project != "shop" {
		t.Fatalf("closed snapshot changed: %+v", event.Event)
	}
}
