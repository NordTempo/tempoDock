package core

import (
	"errors"
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// stoppedWeb deploys web (port 3000, a domain) and stops it.
func stoppedWeb(t *testing.T, h *harness) ServiceRef {
	t.Helper()
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:1")
	if _, err := h.c.StopService(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	h.settleEvents()
	return web
}

// auditActions lists the shop/production/web audit actions, newest first.
func (h *harness) auditActions(action string) []apitypes.AuditEvent {
	h.t.Helper()
	evs, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Project: "shop", Environment: "production", Service: "web", Action: action})
	if err != nil {
		h.t.Fatal(err)
	}
	return evs
}

// assertStopped checks web is stopped: no container, release 1 still
// active with its port, and its domain answering 503.
func (h *harness) assertStopped(web ServiceRef) {
	h.t.Helper()
	svc, err := h.c.GetService(h.ctx, testSystem, web)
	if err != nil {
		h.t.Fatal(err)
	}
	if svc.Status.State != apitypes.ServiceStopped || svc.StoppedAt == nil || svc.StoppedBy != "system" || svc.ActiveRelease == nil || *svc.ActiveRelease != 1 {
		h.t.Fatalf("service = %+v", svc)
	}
	if got := h.rt.Running(); len(got) != 0 {
		h.t.Fatalf("running = %v", got)
	}
	active, err := h.store.GetActiveRelease(h.ctx, svc.ID)
	if err != nil || active.Number != 1 || active.HostPort != 20000 || active.ContainerID != "" {
		h.t.Fatalf("active release = %+v, %v", active, err)
	}
	routes := h.rt.Routes()
	if len(routes) != 1 || routes[0].Stopped != "web" || routes[0].Hostnames[0] != "shop.example.com" {
		h.t.Fatalf("routes = %+v", routes)
	}
}

func TestStopAndStartService(t *testing.T) {
	h := newHarness(t)
	web := stoppedWeb(t, h)
	h.assertStopped(web)

	// Stopping again changes nothing and isn't audited twice.
	if _, err := h.c.StopService(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	if n := len(h.auditActions("service.stop")); n != 1 {
		t.Fatalf("service.stop audited %d times", n)
	}
	if d := h.drift(); len(d) > 0 {
		t.Fatalf("drift reported: %+v", d)
	}

	// Start goes through a health-checked release copying the active one.
	rel, err := h.c.StartService(h.ctx, testSystem, web)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Source != apitypes.SourceRestart || rel.FromRelease != 1 {
		t.Fatalf("start release = %+v", rel)
	}
	if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 2: %s (%s)", r.Status, r.Error)
	}
	h.waitRunning(h.ctr(web, 2))
	svc, _ := h.c.GetService(h.ctx, testSystem, web)
	if svc.Status.State != apitypes.ServiceRunning || svc.StoppedAt != nil || h.status(web, 1) != apitypes.ReleaseSuperseded {
		t.Fatalf("after start: %+v, release 1 %s", svc, h.status(web, 1))
	}
	if routes := h.rt.Routes(); len(routes) != 1 || routes[0].Stopped != "" || routes[0].Upstream != "127.0.0.1:20001" {
		t.Fatalf("routes after start = %+v", routes)
	}
	if len(h.auditActions("service.start")) != 1 {
		t.Fatal("service.start not audited")
	}

	// A running service isn't started again.
	if _, err := h.c.StartService(h.ctx, testSystem, web); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("start a running service: %v", err)
	}
}

func TestFailedStartLeavesServiceStopped(t *testing.T) {
	h := newHarness(t)
	web := stoppedWeb(t, h)
	h.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	rel, err := h.c.StartService(h.ctx, testSystem, web)
	if err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseFailed {
		t.Fatalf("release 2: %s", r.Status)
	}
	h.settleEvents()
	h.assertStopped(web)
}

// Any deploy to a stopped service runs it again; a failed one leaves it
// stopped, even for a stateful service whose deploys replace the
// container.
func TestDeployToStoppedService(t *testing.T) {
	h := newHarness(t)
	db := h.service("db", apitypes.CreateServiceRequest{Port: 5432})
	if _, err := h.c.AddVolume(h.ctx, testSystem, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(db, "postgres:17")
	if _, err := h.c.StopService(h.ctx, testSystem, db); err != nil {
		t.Fatal(err)
	}
	h.settleEvents()

	h.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	if r := h.deploy(db, "postgres:17.1"); r.Status != apitypes.ReleaseFailed {
		t.Fatalf("release 2: %s", r.Status)
	}
	h.settleEvents()
	if got := h.rt.Running(); len(got) != 0 {
		t.Fatalf("a failed deploy started the stopped service: %v", got)
	}
	if svc, _ := h.c.GetService(h.ctx, testSystem, db); svc.Status.State != apitypes.ServiceStopped || svc.StoppedAt == nil {
		t.Fatalf("after a failed deploy: %+v", svc)
	}

	h.rt.FailHealthCheck(nil)
	if r := h.deploy(db, "postgres:17.1"); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 3: %s (%s)", r.Status, r.Error)
	}
	h.waitRunning(h.ctr(db, 3))
	if svc, _ := h.c.GetService(h.ctx, testSystem, db); svc.Status.State != apitypes.ServiceRunning || svc.StoppedAt != nil {
		t.Fatalf("after a deploy: %+v", svc)
	}
}

// Daemon restarts, crashes (with a start in flight) and reconciles never
// start a stopped service, nor report it as drift.
func TestStoppedServiceStaysStopped(t *testing.T) {
	h := newHarness(t)
	web := stoppedWeb(t, h)

	h.restart()
	h.assertStopped(web)

	release := h.rt.HoldHealthChecks()
	defer release()
	rel, err := h.c.StartService(h.ctx, testSystem, web)
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, rel.Number, apitypes.ReleaseHealthChecking)
	h.crash()
	if r := h.status(web, rel.Number); r != apitypes.ReleaseFailed {
		t.Fatalf("interrupted start: %s", r)
	}
	h.assertStopped(web)

	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.c.reconcileServer(h.ctx, ss.env.ServerID, h.rt, true); err != nil {
		t.Fatal(err)
	}
	h.assertStopped(web)
	if d := h.drift(); len(d) > 0 {
		t.Fatalf("drift reported: %+v", d)
	}
}

// A stop whose container couldn't be removed leaves the service stopped,
// and the next start of the daemon removes the container.
func TestStopFinishesAfterFailedRemoval(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:1")
	h.rt.FailStop(errors.New("docker: timeout"))
	if _, err := h.c.StopService(h.ctx, testSystem, web); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("stop with a failing removal: %v", err)
	}
	if got := h.rt.Running(); len(got) != 1 {
		t.Fatalf("running = %v", got)
	}
	h.rt.FailStop(nil)
	h.restart()
	// Recovery removes it before the new daemon follows events.
	h.assertStopped(web)
}

func TestStopServiceRefusals(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.StopService(h.ctx, testSystem, web); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("stop without a release: %v", err)
	}
	if _, err := h.c.StartService(h.ctx, testSystem, web); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("start without being stopped: %v", err)
	}
	h.deploy(web, "app:1")

	release := h.rt.HoldHealthChecks()
	rel, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:2"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, rel.Number, apitypes.ReleaseHealthChecking)
	if _, err := h.c.StopService(h.ctx, testSystem, web); CodeOf(err) != apitypes.CodeDeployInProgress {
		t.Fatalf("stop during a deploy: %v", err)
	}
	release()
	h.wait(web, rel.Number)

	reader := Actor{Kind: "token", OrgID: 1, Name: "ro", TokenID: 1, Scope: apitypes.ScopeRead}
	if _, err := h.c.StopService(h.ctx, reader, web); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read token stops: %v", err)
	}
	if _, err := h.c.StopService(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.StartService(h.ctx, reader, web); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("read token starts: %v", err)
	}
}

// A stopped service's logs are its active release's archived output: the
// stop archived it before removing the container.
func TestStoppedServiceLogs(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	h.rt.AddLogs(h.ctr(web, 1), "listening on :8080", "stopping")
	if _, err := h.c.StopService(h.ctx, testSystem, web); err != nil {
		t.Fatal(err)
	}
	got, err := h.readLogs(testSystem, web, LogQuery{Tail: 10})
	if err != nil || !slices.Equal(got, []string{"listening on :8080", "stopping"}) {
		t.Fatalf("logs of the stopped service = %v, %v", got, err)
	}
}

// A snapshot restored in place into a stopped service leaves it stopped;
// a stop waits while one is being restored.
func TestStoppedServiceAndRestoreInPlace(t *testing.T) {
	h := newHarness(t)
	h.snapshotJobs(nil)
	app := h.volumeService("app")
	s := h.snapshot(app)

	copying, release := make(chan struct{}), make(chan struct{})
	h.snapshotJobs(func(spec node.JobSpec) *node.JobResult {
		if spec.Script == snapshotRestoreScript {
			close(copying)
			<-release
		}
		return nil
	})
	res := h.restoreSnapshot(app, s.ID, apitypes.RestoreSnapshotRequest{InPlace: true})
	<-copying
	if _, err := h.c.StopService(h.ctx, testSystem, app); CodeOf(err) != apitypes.CodeConflict {
		t.Errorf("stop during an in-place restore: %v", err)
	}
	close(release)
	h.waitRestore(app, res.Restore.ID)

	h.snapshotJobs(nil)
	if _, err := h.c.StopService(h.ctx, testSystem, app); err != nil {
		t.Fatal(err)
	}
	res = h.restoreSnapshot(app, s.ID, apitypes.RestoreSnapshotRequest{InPlace: true})
	if r := h.waitRestore(app, res.Restore.ID); r.Status != apitypes.BackupSucceeded {
		t.Fatalf("restore into the stopped service = %+v", r)
	}
	if running := h.rt.Running(); len(running) != 0 {
		t.Fatalf("a restore started the stopped service: running %v", running)
	}
	if svc, _ := h.c.GetService(h.ctx, testSystem, app); svc.StoppedAt == nil || svc.Status.State != apitypes.ServiceStopped {
		t.Fatalf("after the restore: %+v", svc.Status)
	}
}
