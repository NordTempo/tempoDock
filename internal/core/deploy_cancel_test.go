package core

import (
	"archive/tar"
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// A deploy canceled before its cutover (ADR 0086) ends as canceled: its
// container is gone, the release that served keeps serving, and nothing
// treats it as a failure: no alert (live or reconciled), no diagnosis,
// no last failed deploy.
func TestCancelDeployKeepsTheServingRelease(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	release := h.rt.HoldHealthChecks()
	defer release()
	r2, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:2"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, r2.Number, apitypes.ReleaseHealthChecking)

	got, err := h.c.CancelRelease(h.ctx, testSystem, web, r2.Number)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != apitypes.ReleaseCanceled || got.ErrorCode != apitypes.CodeDeployCanceled || got.Error != "canceled by system" || got.Diagnosis != nil {
		t.Fatalf("canceled release = %+v", got)
	}
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "reading releases")
	if err != nil {
		t.Fatal(err)
	}
	if ids, _ := h.rt.ReleaseContainers(h.ctx, h.c.labels(ss, h.releaseID(web, r2.Number))); len(ids) != 0 {
		t.Fatalf("release 2's containers are left: %v", ids)
	}
	if svc, _ := h.c.GetService(h.ctx, testSystem, web); svc.ActiveRelease == nil || *svc.ActiveRelease != 1 {
		t.Fatalf("active release = %v, want 1", svc.ActiveRelease)
	}
	full, _ := h.c.GetRelease(h.ctx, testSystem, web, r2.Number)
	if last := full.Events[len(full.Events)-1].Message; last != "Release 2 was canceled by system; release 1 keeps serving" {
		t.Fatalf("last event = %q", last)
	}
	if events, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "release.cancel"}); len(events) != 1 {
		t.Fatalf("audit = %+v", events)
	}

	if !h.c.alertsHandled(h.ctx) {
		t.Fatal("alerts not handled")
	}
	h.c.reconcileAlerts(h.ctx, time.Now())
	h.openAlert("deploy:service:1", false)
	ov, err := h.c.Overview(h.ctx, testSystem, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if so := ov.Environments[0].Services[0]; so.LastFailedDeploy != nil || so.LatestRelease.Status != apitypes.ReleaseCanceled {
		t.Fatalf("overview: last failed %+v, latest %+v", so.LastFailedDeploy, so.LatestRelease)
	}

	// Canceling again, or a release that went live, is refused.
	if _, err := h.c.CancelRelease(h.ctx, testSystem, web, r2.Number); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("cancel a canceled release: %v", err)
	}
	if _, err := h.c.CancelRelease(h.ctx, testSystem, web, 1); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "dock rollback") {
		t.Fatalf("cancel the active release: %v", err)
	}
}

// A cancel that comes once the cutover began is refused, and the release
// goes live: traffic never moves halfway.
func TestCancelRacingCutoverIsRefused(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	held, release := h.rt.HoldApplyRoutes(func([]node.Route) bool { return true })
	defer release()
	r2, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:2"})
	if err != nil {
		t.Fatal(err)
	}
	<-held
	if _, err := h.c.CancelRelease(h.ctx, testSystem, web, r2.Number); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "too late to cancel") {
		t.Fatalf("cancel during the cutover: %v", err)
	}
	release()
	if got := h.wait(web, r2.Number); got.Status != apitypes.ReleaseActive {
		t.Fatalf("release 2 = %s, want active", got.Status)
	}
}

// Canceling needs the deploy permission; a read token is refused and the
// deploy goes on.
func TestCancelNeedsDeploy(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	release := h.rt.HoldHealthChecks()
	defer release()
	r1, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:1"})
	if err != nil {
		t.Fatal(err)
	}
	reader := h.token(testSystem, apitypes.CreateTokenRequest{Name: "reader", Scope: apitypes.ScopeRead})
	if _, err := h.c.CancelRelease(h.ctx, reader, web, r1.Number); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read token: %v", err)
	}
	release()
	if got := h.wait(web, r1.Number); got.Status != apitypes.ReleaseActive {
		t.Fatalf("release 1 = %s, want active", got.Status)
	}
}

// A deploy waiting for the platform registry (held by a migration) can
// be canceled: the wait ends with it.
func TestCancelWhileWaitingForTheRegistry(t *testing.T) {
	p, _ := labPool(t)
	p.withRegistry(&fakeRegistry{})
	p.buildsProduction("homelab")
	web := p.createService("production", "web", apitypes.CreateServiceRequest{})
	release, err := p.c.holdRegistry()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: 11, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("FROM app:1\n"))
	_ = tw.Close()
	rel, err := p.c.DeploySource(p.ctx, testSystem, web, &buf, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the deploy to wait for the registry", func() bool {
		r, err := p.c.GetRelease(p.ctx, testSystem, web, rel.Number)
		return err == nil && slices.ContainsFunc(r.Events, func(e apitypes.ReleaseEvent) bool {
			return strings.HasPrefix(e.Message, "Waiting for the platform registry")
		})
	})
	got, err := p.c.CancelRelease(p.ctx, testSystem, web, 0)
	if err != nil || got.Status != apitypes.ReleaseCanceled {
		t.Fatalf("cancel = %+v, %v", got, err)
	}
}
