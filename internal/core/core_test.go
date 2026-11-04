package core

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/config"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/fake"
	"github.com/NordTempo/tempoDock/internal/secrets"
	"github.com/NordTempo/tempoDock/internal/store"
)

// testSystem is the system actor in the test org, which newHarness creates
// first (ID 1), so tests can address projects by name.
var testSystem = System.InOrg(1)

type harness struct {
	t     *testing.T
	ctx   context.Context
	c     *Core
	rt    *fake.Runtime
	store *store.Store
	opts  Options
	// nodes, when set, are plugged in with SetNodes on every start.
	nodes Nodes
	// dns answers custom domain verification's TXT lookups (ADR 0042).
	dns *fakeDNS
}

// openTestStore opens a database with the test org (ID 1) in it.
func openTestStore(t testing.TB, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrg(context.Background(), store.CreateOrgParams{Slug: "test", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	return st
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st := openTestStore(t, filepath.Join(dir, "dock.db"))
	box, err := secrets.New(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.BaseDomain = "apps.test"
	cfg.PublicIP = "203.0.113.10"
	// The analytics store's binary: a development build has no release
	// to download it from.
	cfg.Analytics.StoreBinary = filepath.Join(dir, "dock-analytics")
	if err := os.WriteFile(cfg.Analytics.StoreBinary, []byte("store"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, ctx: ctx, rt: fake.New(), store: st, dns: &fakeDNS{}}
	h.opts = Options{Config: cfg, Store: st, Secrets: box, Runtime: h.rt, Logger: slog.New(slog.DiscardHandler), CutoverDrain: time.Nanosecond, LookupTXT: h.dns.lookupTXT}
	h.c = h.start()
	t.Cleanup(func() {
		h.c.Close()
		_ = st.Close()
	})
	return h
}

func (h *harness) start() *Core {
	h.t.Helper()
	c, err := New(h.ctx, h.opts)
	if err != nil {
		h.t.Fatal(err)
	}
	if h.nodes != nil {
		c.SetNodes(h.nodes)
	}
	if err := c.Start(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	return c
}

// restart stops the daemon gracefully and starts a new one on the same
// database and runtime.
func (h *harness) restart() {
	h.t.Helper()
	h.c.Close()
	h.c = h.start()
}

// crash starts a new daemon without stopping the current one, like a
// process that died mid-work: the old one's goroutines are abandoned (and
// closed at cleanup).
func (h *harness) crash() {
	h.t.Helper()
	h.t.Cleanup(h.c.Close)
	h.c = h.start()
}

// service creates project shop (with its production environment) if
// needed, and a service in it.
func (h *harness) service(name string, req apitypes.CreateServiceRequest) ServiceRef {
	h.t.Helper()
	if _, err := h.c.GetProject(h.ctx, testSystem, "shop"); err != nil {
		if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
			h.t.Fatal(err)
		}
	}
	req.Name = name
	if _, err := h.c.CreateService(h.ctx, testSystem, EnvRef{"shop", "production"}, req); err != nil {
		h.t.Fatal(err)
	}
	return ServiceRef{"shop", "production", name}
}

func (h *harness) deploy(ref ServiceRef, image string) apitypes.Release {
	h.t.Helper()
	rel, err := h.c.DeployImage(h.ctx, testSystem, ref, apitypes.DeployImageRequest{Image: image})
	if err != nil {
		h.t.Fatal(err)
	}
	return h.wait(ref, rel.Number)
}

// wait waits until a release finishes deploying and returns it.
func (h *harness) wait(ref ServiceRef, number int64) apitypes.Release {
	h.t.Helper()
	return h.waitRelease(ref, number, "finished", func(s apitypes.ReleaseStatus) bool { return s.Done() })
}

// waitStatus waits until a release has status want.
func (h *harness) waitStatus(ref ServiceRef, number int64, want apitypes.ReleaseStatus) {
	h.t.Helper()
	h.waitRelease(ref, number, string(want), func(s apitypes.ReleaseStatus) bool { return s == want })
}

// waitRelease waits until a release's status satisfies ok and returns the
// release, failing the test after 5 seconds. It doesn't poll: every
// release status change is followed by an event for its service on the
// bus, so it reads the release again after each of those. Subscribing
// before the first read means no change can fall in between.
func (h *harness) waitRelease(ref ServiceRef, number int64, want string, ok func(apitypes.ReleaseStatus) bool) apitypes.Release {
	h.t.Helper()
	sub := h.c.bus.subscribe(func(e busEvent) bool {
		return e.ev.Project == ref.Project && e.ev.Environment == ref.Environment && e.ev.Service == ref.Service
	})
	defer sub.close()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		rel, err := h.c.GetRelease(h.ctx, testSystem, ref, number)
		if err != nil {
			h.t.Fatal(err)
		}
		if ok(rel.Status) {
			return rel
		}
		select {
		case <-sub.ch:
		case <-timeout.C:
			h.t.Fatalf("release %d is %s, want %s", number, rel.Status, want)
		}
	}
}

func (h *harness) status(ref ServiceRef, number int64) apitypes.ReleaseStatus {
	h.t.Helper()
	rel, err := h.c.GetRelease(h.ctx, testSystem, ref, number)
	if err != nil {
		h.t.Fatal(err)
	}
	return rel.Status
}

// ctr is the container name of a service's release.
func (h *harness) ctr(ref ServiceRef, number int64) string {
	h.t.Helper()
	ss, err := h.c.serviceFor(h.ctx, testSystem, ref, apitypes.ScopeRead, "testing")
	if err != nil {
		h.t.Fatal(err)
	}
	return containerName(ss, number)
}

func indexOf(ops []string, op string) int { return slices.Index(ops, op) }

// waitRunning waits for the set of running containers to settle on want;
// old containers are stopped after a release goes live.
func (h *harness) waitRunning(want ...string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Equal(h.rt.Running(), want) {
		if time.Now().After(deadline) {
			h.t.Fatalf("running = %v, want %v", h.rt.Running(), want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestDeployActivatesAndSwitchesRoutes(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "Shop.Example.com"}); err != nil {
		t.Fatal(err)
	}

	r1 := h.deploy(web, "nginx:1")
	if r1.Status != apitypes.ReleaseActive {
		t.Fatalf("release 1: %s (%s)", r1.Status, r1.Error)
	}
	routes := h.rt.Routes()
	if len(routes) != 1 || routes[0].Hostnames[0] != "shop.example.com" || routes[0].Upstream != "127.0.0.1:20000" {
		t.Fatalf("routes after release 1: %+v", routes)
	}

	r2 := h.deploy(web, "nginx:2")
	if r2.Status != apitypes.ReleaseActive || h.status(web, 1) != apitypes.ReleaseSuperseded {
		t.Fatalf("release 2 %s, release 1 %s", r2.Status, h.status(web, 1))
	}
	if routes := h.rt.Routes(); routes[0].Upstream != "127.0.0.1:20001" {
		t.Fatalf("routes after release 2: %+v", routes)
	}
	h.waitRunning(h.ctr(web, 2))
	// Zero downtime: the new container starts and gets traffic before the
	// old one stops.
	ops := h.rt.Ops()
	start, routed, stop := indexOf(ops, "start "+h.ctr(web, 2)), indexOf(ops, "routes shop.example.com->127.0.0.1:20001"), indexOf(ops, "stop "+h.ctr(web, 1))
	if start < 0 || start > routed || routed > stop {
		t.Fatalf("want start < routes < stop, ops = %v", ops)
	}
	spec, _ := h.rt.Spec(h.ctr(web, 2))
	if spec.Env["PORT"] != "3000" || spec.Env["DOCK_PUBLIC_DOMAIN"] != "shop.example.com" || spec.HostPort != 20001 {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestFailedHealthCheckKeepsOldReleaseServing(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000, HealthCheckPath: "/healthz"})
	h.deploy(web, "app:good")
	before := h.rt.Routes()

	h.rt.FailHealthCheck(func(s node.ContainerSpec) error {
		if s.Image == "app:bad" || strings.HasSuffix(s.Name, "_r2") {
			return errors.New("GET /healthz: 500")
		}
		return nil
	})
	r2 := h.deploy(web, "app:bad")
	if r2.Status != apitypes.ReleaseFailed || r2.ErrorCode != apitypes.CodeHealthCheckFailed {
		t.Fatalf("release 2: %s %s %q", r2.Status, r2.ErrorCode, r2.Error)
	}
	if !strings.Contains(r2.Error, "/healthz") {
		t.Fatalf("error should name the check: %q", r2.Error)
	}
	if h.status(web, 1) != apitypes.ReleaseActive {
		t.Fatal("release 1 should still be active")
	}
	if got := h.rt.Running(); !slices.Equal(got, []string{h.ctr(web, 1)}) {
		t.Fatalf("running = %v", got)
	}
	if !reflect.DeepEqual(h.rt.Routes(), before) {
		t.Fatalf("routes changed: %+v", h.rt.Routes())
	}
}

func TestCrashRecoveryFailsInFlightReleases(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")

	release := h.rt.HoldHealthChecks()
	defer release()
	rel, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:2"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, rel.Number, apitypes.ReleaseHealthChecking)

	// Simulate a crash: the old process vanishes without cleaning up. Its
	// deploy goroutine stays blocked (and is abandoned) while a new process
	// starts on the same database and runtime.
	crashed := h.c
	h.c = h.start()
	defer func() { h.c = crashed }() // Cleanup closes the original

	r2, err := h.c.GetRelease(h.ctx, testSystem, web, rel.Number)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Status != apitypes.ReleaseFailed || r2.Error != "daemon restarted during deploy" {
		t.Fatalf("release 2: %s %q", r2.Status, r2.Error)
	}
	if h.status(web, 1) != apitypes.ReleaseActive {
		t.Fatal("release 1 should still be active")
	}
	if got := h.rt.Running(); !slices.Equal(got, []string{h.ctr(web, 1)}) {
		t.Fatalf("running = %v", got)
	}
	h.c.Close()
}

func TestRollbackReusesImageAndConfig(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"MODE": "one"}}); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:1")
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"MODE": "two"}}); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:2")

	rel, err := h.c.Rollback(h.ctx, testSystem, web, 1)
	if err != nil {
		t.Fatal(err)
	}
	r3 := h.wait(web, rel.Number)
	if r3.Status != apitypes.ReleaseActive || r3.Source != apitypes.SourceRollback || r3.FromRelease != 1 || r3.ImageRef != "app:1" {
		t.Fatalf("rollback release: %+v", r3)
	}
	spec, _ := h.rt.Spec(h.ctr(web, 3))
	if spec.Env["MODE"] != "one" {
		t.Fatalf("rollback should use release 1's config, MODE = %q", spec.Env["MODE"])
	}
	if _, err := h.c.Rollback(h.ctx, testSystem, web, 9); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("rollback to missing release: %v", err)
	}
}

func TestStatefulServiceIsRecreated(t *testing.T) {
	h := newHarness(t)
	db := h.service("db", apitypes.CreateServiceRequest{Port: 5432})
	if _, err := h.c.AddVolume(h.ctx, testSystem, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/var/lib/postgresql/data"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(db, "postgres:17") // applies the staged volume
	r2 := h.deploy(db, "postgres:17.1")
	if r2.Status != apitypes.ReleaseActive || len(r2.Config.Mounts) != 1 {
		t.Fatalf("release 2: %+v", r2)
	}
	ops := h.rt.Ops()
	if stop, start := indexOf(ops, "stop "+h.ctr(db, 1)), indexOf(ops, "start "+h.ctr(db, 2)); stop < 0 || stop > start {
		t.Fatalf("stateful deploy must stop the old container first, ops = %v", ops)
	}

	// A failed stateful deploy brings the previous release back.
	h.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("not ready") })
	r3 := h.deploy(db, "postgres:broken")
	h.rt.FailHealthCheck(nil)
	if r3.Status != apitypes.ReleaseFailed || h.status(db, 2) != apitypes.ReleaseActive {
		t.Fatalf("release 3 %s, release 2 %s", r3.Status, h.status(db, 2))
	}
	if got := h.rt.Running(); !slices.Equal(got, []string{h.ctr(db, 2)}) {
		t.Fatalf("running = %v", got)
	}
}

// settleEvents waits until the runtime events of every container the daemon
// removed (up to their destroy event) have been handled. In a synctest
// bubble, synctest.Wait does the same without polling.
func (h *harness) settleEvents() {
	h.t.Helper()
	eventually(h.t, "the removed containers' events to be handled", func() bool {
		h.c.statusMu.Lock()
		defer h.c.statusMu.Unlock()
		return len(h.c.removing) == 0
	})
}

// drift lists drift entries: the org's (in a project) and the platform's.
func (h *harness) drift() []apitypes.AuditEvent {
	h.t.Helper()
	var all []apitypes.AuditEvent
	for _, platform := range []bool{false, true} {
		evs, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "reconcile.drift", Platform: platform})
		if err != nil {
			h.t.Fatal(err)
		}
		all = append(all, evs...)
	}
	return all
}

// The die and destroy events of containers the daemon stops itself (the
// active one in a stateful recreate, an archived service's) are neither a
// crash nor drift. It runs in a synctest bubble: synctest.Wait returns once
// every daemon goroutine is idle, so all runtime events have been handled
// when the status and the drift log are checked.
func TestOwnRemovalsAreNotDrift(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		db := h.service("db", apitypes.CreateServiceRequest{Port: 5432})
		if _, err := h.c.AddVolume(h.ctx, testSystem, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
			t.Fatal(err)
		}
		h.deploy(db, "postgres:17")
		h.deploy(db, "postgres:17.1")
		synctest.Wait()
		if svc, _ := h.c.GetService(h.ctx, testSystem, db); svc.Status.State != apitypes.ServiceRunning {
			t.Fatalf("status after recreate = %+v", svc.Status)
		}
		if _, err := h.c.ArchiveService(h.ctx, testSystem, db); err != nil {
			t.Fatal(err)
		}
		h.waitRunning()
		synctest.Wait()
		if svc, _ := h.c.GetService(h.ctx, testSystem, db); svc.Status.State != apitypes.ServiceNone {
			t.Fatalf("status after archive = %+v", svc.Status)
		}
		if d := h.drift(); len(d) > 0 {
			t.Fatalf("drift reported: %+v", d)
		}
	})
}

// Containers labelled for another control plane on the same Docker host
// are never removed or reported as drift, even with the same IDs in their
// labels. This control plane's containers from before the label (all
// other labels matching) are still its own.
func TestOtherControlPlanesAreIgnored(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	r1 := h.deploy(web, "app:1")
	r2 := h.deploy(web, "app:2")
	if spec, _ := h.rt.Spec(h.ctr(web, 2)); spec.Labels.ControlPlane == "" || spec.Labels.ControlPlane != h.c.install {
		t.Fatalf("container labels = %+v, install ID %q", spec.Labels, h.c.install)
	}
	ss, err := h.c.loadService(h.ctx, 1, web)
	if err != nil {
		t.Fatal(err)
	}
	theirs := func(releaseID int64) node.Labels {
		l := h.c.labels(ss, releaseID)
		l.ControlPlane = "other"
		return l
	}
	legacy := h.c.labels(ss, r1.ID)
	legacy.ControlPlane = ""
	for _, ct := range []node.Container{
		{ID: "legacy", Name: "legacy", Labels: legacy},
		{ID: "theirs-1", Name: "theirs-1", Labels: theirs(r1.ID)},
		{ID: "theirs-2", Name: "theirs-2", Labels: theirs(r2.ID)},
		{ID: "theirs-x", Name: "theirs-x", Labels: node.Labels{ControlPlane: "other", Project: "9", Environment: "9", Service: "9", Release: "999"}},
	} {
		ct.State = node.StateRunning
		h.rt.AddContainer(ct)
	}
	h.restart() // removes leftovers of retired releases, reports drift
	h.deploy(web, "app:3")
	want := []string{h.ctr(web, 3), "theirs-1", "theirs-2", "theirs-x"}
	slices.Sort(want)
	h.waitRunning(want...)
	if d := h.drift(); len(d) > 0 {
		t.Fatalf("drift reported: %+v", d)
	}
}

// Archiving answers without waiting for the container to stop: routes go
// first, the container is stopped in the background, and its port is only
// freed once it is gone.
func TestArchiveStopsInBackground(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "web.example.com"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:1")
	release := h.rt.HoldStops()
	defer release()

	svc, err := h.c.ArchiveService(h.ctx, testSystem, web)
	if err != nil {
		t.Fatal(err)
	}
	if svc.ArchivedAt == nil || svc.Status.State != apitypes.ServiceNone || svc.ActiveRelease != nil {
		t.Fatalf("archived service = %+v", svc)
	}
	if routes := h.rt.Routes(); len(routes) != 0 {
		t.Fatalf("routes = %+v", routes)
	}
	if got := h.rt.Running(); !slices.Equal(got, []string{h.ctr(web, 1)}) {
		t.Fatalf("running = %v; the stop should still be in progress", got)
	}
	ss, _ := h.c.loadService(h.ctx, 1, web)
	if ports, _ := h.c.store.ListUsedHostPorts(h.ctx, ss.env.ServerID); len(ports) != 1 {
		t.Fatalf("port freed while the container still runs: %v", ports)
	}

	release()
	h.waitRunning()
	// The port is freed before the stop is recorded as an event.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ports, _ := h.c.store.ListUsedHostPorts(h.ctx, ss.env.ServerID)
		r1, _ := h.c.GetRelease(h.ctx, testSystem, web, 1)
		stopped := slices.ContainsFunc(r1.Events, func(e apitypes.ReleaseEvent) bool {
			return strings.HasPrefix(e.Message, "Stopped release 1")
		})
		if len(ports) == 0 && stopped {
			if r1.Status != apitypes.ReleaseSuperseded {
				t.Fatalf("release 1 = %s", r1.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ports %v, release 1 = %s, events %+v", ports, r1.Status, r1.Events)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Archiving a project archives every service in every environment the same
// way (volumes kept), hides the project from lists and takes nothing new.
func TestArchiveProject(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	db := h.service("db", apitypes.CreateServiceRequest{})
	if _, err := h.c.AddVolume(h.ctx, testSystem, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	staging := ServiceRef{"shop", "staging", "web"}
	if _, err := h.c.CreateService(h.ctx, testSystem, staging.Env(), apitypes.CreateServiceRequest{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:1")
	h.deploy(db, "postgres:17")
	h.deploy(staging, "app:1")

	deployer := Actor{Name: "ci", Kind: "token", Scope: apitypes.ScopeDeploy}
	if _, err := h.c.ArchiveProject(h.ctx, deployer, "shop"); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy scope archived a project: %v", err)
	}
	if nets := h.rt.Networks(); len(nets) != 2 {
		t.Fatalf("networks = %v", nets)
	}
	p, err := h.c.ArchiveProject(h.ctx, testSystem, "shop")
	if err != nil || p.ArchivedAt == nil {
		t.Fatalf("archive = %+v, %v", p, err)
	}
	h.waitRunning()
	// The environments' networks go once their containers are stopped, so
	// their subnets return to the pool.
	deadline := time.Now().Add(5 * time.Second)
	for len(h.rt.Networks()) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("networks left after the archive: %v", h.rt.Networks())
		}
		time.Sleep(2 * time.Millisecond)
	}
	for _, ref := range []ServiceRef{web, db, staging} {
		if svc, _ := h.c.GetService(h.ctx, testSystem, ref); svc.ArchivedAt == nil || svc.ActiveRelease != nil {
			t.Errorf("%s = %+v", ref, svc)
		}
	}
	if vols, _ := h.c.ListVolumes(h.ctx, testSystem, db); len(vols) != 1 {
		t.Errorf("volumes = %+v", vols)
	}
	if ps, _ := h.c.ListProjects(h.ctx, testSystem, false); len(ps) != 0 {
		t.Errorf("listed by default: %+v", ps)
	}
	if ps, _ := h.c.ListProjects(h.ctx, testSystem, true); len(ps) != 1 || ps[0].ArchivedAt == nil {
		t.Errorf("listed with archived: %+v", ps)
	}
	if _, err := h.c.CreateService(h.ctx, testSystem, web.Env(), apitypes.CreateServiceRequest{Name: "new"}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("service created in archived project: %v", err)
	}
	if _, err := h.c.ArchiveProject(h.ctx, testSystem, "shop"); err != nil {
		t.Errorf("archiving again: %v", err)
	}
}

// An OOM kill of the running container shows in the service's status with
// the limit it hit.
func TestOOMKillStatusHasLimit(t *testing.T) {
	h := newHarness(t)
	mem := 128
	web := h.service("web", apitypes.CreateServiceRequest{MemoryMB: mem})
	h.deploy(web, "app:1")
	ss, _ := h.c.loadService(h.ctx, 1, web)
	active, _ := h.c.store.GetActiveRelease(h.ctx, ss.svc.ID)
	h.rt.Emit(node.ContainerEvent{ContainerID: active.ContainerID, Action: "oom"})
	h.rt.Emit(node.ContainerEvent{ContainerID: active.ContainerID, Action: "die", ExitCode: 137})
	deadline := time.Now().Add(5 * time.Second)
	for {
		svc, _ := h.c.GetService(h.ctx, testSystem, web)
		if s := svc.Status; s.State == apitypes.ServiceCrashed {
			if !s.OOMKilled || s.MemoryLimitMB != mem || s.ExitCode != 137 {
				t.Fatalf("status = %+v", s)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v", svc.Status)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestVariableReferences(t *testing.T) {
	h := newHarness(t)
	api := h.service("api", apitypes.CreateServiceRequest{})
	web := h.service("web", apitypes.CreateServiceRequest{})
	env := web.Env()
	set := func(service string, vars map[string]string) {
		t.Helper()
		if _, err := h.c.SetVariables(h.ctx, testSystem, env, apitypes.SetVariablesRequest{Service: service, Set: vars}); err != nil {
			t.Fatal(err)
		}
	}
	set("", map[string]string{"REGION": "eu"})
	set("api", map[string]string{"URL": "http://${{api.DOCK_PRIVATE_DOMAIN}}:${{ api.PORT }}"})
	set("web", map[string]string{"API_URL": "${{api.URL}}/v1", "WHERE": "${{shared.REGION}}"})
	if _, err := h.c.ApplyChanges(h.ctx, testSystem, env); err != nil {
		t.Fatal(err)
	}
	h.deploy(api, "api:1")
	h.deploy(web, "web:1")
	spec, _ := h.rt.Spec(h.ctr(web, 1))
	if spec.Env["API_URL"] != "http://api:8080/v1" || spec.Env["WHERE"] != "eu" || spec.Env["REGION"] != "eu" {
		t.Fatalf("env = %v", spec.Env)
	}

	// An unknown reference fails the apply, names the problem, and leaves
	// the change staged.
	set("web", map[string]string{"DB": "${{postgres.DATABASE_URL}}"})
	_, err := h.c.ApplyChanges(h.ctx, testSystem, env)
	if CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), `no service "postgres"`) || !strings.Contains(err.Error(), "DB") {
		t.Fatalf("apply with unknown reference: %v", err)
	}
	pending, _ := h.c.GetChanges(h.ctx, testSystem, env, false)
	if pending.Count != 1 {
		t.Fatalf("change should stay staged: %+v", pending)
	}
	set("web", map[string]string{"DB": "${{api.NOPE}}"})
	if _, err := h.c.ApplyChanges(h.ctx, testSystem, env); err == nil || !strings.Contains(err.Error(), "api has no variable NOPE") {
		t.Fatalf("apply with unknown key: %v", err)
	}
}

func TestOneDeployPerService(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	release := h.rt.HoldHealthChecks()
	rel, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:2"})
	if CodeOf(err) != apitypes.CodeDeployInProgress {
		t.Fatalf("second deploy: %v", err)
	}
	release()
	if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 1: %s", r.Status)
	}
}

func TestStagedChangesCreateConfigRelease(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")

	res, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"FOO": "bar"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending.Count != 1 || res.Applied != nil {
		t.Fatalf("staged: %+v", res)
	}
	if rels, _ := h.c.ListReleases(h.ctx, testSystem, web, 10); len(rels) != 1 {
		t.Fatalf("staging must not deploy, releases = %d", len(rels))
	}

	applied, err := h.c.ApplyChanges(h.ctx, testSystem, web.Env())
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Releases) != 1 || applied.Releases[0].Source != apitypes.SourceConfig {
		t.Fatalf("applied: %+v", applied)
	}
	r2 := h.wait(web, applied.Releases[0].Number)
	if r2.Status != apitypes.ReleaseActive || r2.ImageRef != "app:1" || !slices.Contains(r2.Config.VariableKeys, "FOO") {
		t.Fatalf("config release: %+v", r2)
	}

	// A deploy takes the service's staged settings with it.
	port := 4000
	if _, err := h.c.UpdateService(h.ctx, testSystem, web, apitypes.UpdateServiceRequest{Port: &port}); err != nil {
		t.Fatal(err)
	}
	r3 := h.deploy(web, "app:2")
	if r3.Config.Port != 4000 {
		t.Fatalf("deploy should apply staged port, got %d", r3.Config.Port)
	}
	if pending, _ := h.c.GetChanges(h.ctx, testSystem, web.Env(), false); pending.Count != 0 {
		t.Fatalf("nothing should be staged after deploy: %+v", pending)
	}
}

func TestSourceDeployUsesRepoFile(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range map[string]string{
		"dock.toml": "[deploy]\nport = 3000\nstart_command = \"node server.js\"\n",
		"server.js": "console.log('hi')",
	} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	size := buf.Len()

	rel, err := h.c.DeploySource(h.ctx, testSystem, web, &buf, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	r := h.wait(web, rel.Number)
	// dock.toml is read on the way to disk; the build still gets it all.
	logs, err := h.c.BuildLog(h.ctx, testSystem, web, rel.Number)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(logs); !strings.Contains(string(b), fmt.Sprintf("Received %d bytes", size)) {
		t.Fatalf("build log: %s", b)
	}
	_ = logs.Close()
	if r.Status != apitypes.ReleaseActive || r.Config.Port != 3000 || r.Config.StartCommand != "node server.js" {
		t.Fatalf("release: %+v", r)
	}
	if !slices.Equal(r.Config.FromRepoFile, []string{"port", "start_command"}) {
		t.Fatalf("from_repo_file = %v", r.Config.FromRepoFile)
	}

	if _, err := h.c.DeploySource(h.ctx, testSystem, web, strings.NewReader(strings.Repeat("x", 100)), 10); CodeOf(err) != apitypes.CodeTooLarge {
		t.Fatalf("oversized upload: %v", err)
	}
	notTar := strings.NewReader(strings.Repeat("this is a zip file, honest\n", 40))
	if _, err := h.c.DeploySource(h.ctx, testSystem, web, notTar, 1<<20); CodeOf(err) != apitypes.CodeValidation || err.Error() != "upload is not a tar or tar.gz archive" {
		t.Fatalf("non-tar upload: %v", err)
	}
}

// /console is a page and /<org>/audit an org's page, so neither can be
// an org's or a project's address.
func TestNamesAvoidUIPages(t *testing.T) {
	h := newHarness(t)
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "audit"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("creating project audit: got %v, want a validation error", err)
	}
	ada := h.in(h.member("ada@x", map[string]apitypes.Scope{"test": apitypes.ScopeRead}), "")
	if _, err := h.c.CreateOrg(h.ctx, ada, apitypes.CreateOrgRequest{Slug: "console"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("creating org console: got %v, want a validation error", err)
	}
}

// eventually fails the test unless ok becomes true within 5 seconds.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
