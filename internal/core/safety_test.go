package core

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/store"
)

// Project "a" env "b-c" and project "a-b" env "c" once shared the Docker
// network "dock-a-b-c" (and container and volume names), letting one
// project's containers reach the other's.
func TestResourceNamesDontCollideAcrossProjects(t *testing.T) {
	h := newHarness(t)
	refs := []ServiceRef{{"a", "b-c", "s-v"}, {"a-b", "c", "s-v"}}
	for _, ref := range refs {
		if _, err := h.c.CreateProject(h.ctx, System, apitypes.CreateProjectRequest{Name: ref.Project}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.c.CreateEnvironment(h.ctx, System, ref.Project, apitypes.CreateEnvironmentRequest{Name: ref.Environment}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.c.CreateService(h.ctx, System, ref.Env(), apitypes.CreateServiceRequest{Name: ref.Service}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.c.AddVolume(h.ctx, System, ref, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
			t.Fatal(err)
		}
		if r := h.deploy(ref, "app:1"); r.Status != apitypes.ReleaseActive {
			t.Fatalf("%s: %s %s", ref, r.Status, r.Error)
		}
	}
	a, okA := h.rt.Spec(h.ctr(refs[0], 1))
	b, okB := h.rt.Spec(h.ctr(refs[1], 1))
	if !okA || !okB || a.Name == b.Name {
		t.Fatalf("container names: %q %q", a.Name, b.Name)
	}
	if a.Network == b.Network || a.Mounts[0].Volume == b.Mounts[0].Volume {
		t.Fatalf("shared resources: networks %q %q, volumes %q %q", a.Network, b.Network, a.Mounts[0].Volume, b.Mounts[0].Volume)
	}
}

// A deploy that loaded the service just before it was archived must not
// go live: startRelease re-checks inside its transaction.
func TestDeployRacingArchiveIsRefused(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	ss, err := h.c.serviceFor(h.ctx, System, web, apitypes.ScopeDeploy, "deploying")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.ArchiveService(h.ctx, System, web); err != nil {
		t.Fatal(err)
	}
	_, err = h.c.startRelease(h.ctx, System, ss, releaseSpec{source: apitypes.SourceImage, imageRef: "app:1"})
	if CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("deploy after archive: %v", err)
	}
	if rels, _ := h.c.ListReleases(h.ctx, System, web, 10); len(rels) != 0 {
		t.Fatalf("releases = %d, want none", len(rels))
	}
}

// waitOp waits until the runtime has seen op.
func (h *harness) waitOp(op string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(h.rt.Ops(), op) {
		if time.Now().After(deadline) {
			h.t.Fatalf("no %q in ops %v", op, h.rt.Ops())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A replaced container keeps its host port until it is really gone: a stop
// that fails (or a drain cut short by a crash) must not hand the port to
// the next release, and the next start cleans the container up.
func TestHostPortHeldUntilContainerRemoved(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	h.deploy(web, "app:1") // port 20000
	h.rt.FailStop(errors.New("stuck"))
	h.deploy(web, "app:2") // port 20001; release 1 won't stop
	h.waitOp("stop " + h.ctr(web, 1))
	h.rt.FailStop(nil)

	if r := h.deploy(web, "app:3"); r.Status != apitypes.ReleaseActive {
		t.Fatalf("release 3: %s %s", r.Status, r.Error)
	}
	if spec, _ := h.rt.Spec(h.ctr(web, 3)); spec.HostPort != 20002 {
		t.Fatalf("release 3 got port %d; 20000 is still bound by release 1", spec.HostPort)
	}

	h.restart()
	h.waitRunning(h.ctr(web, 3))
	h.deploy(web, "app:4")
	if spec, _ := h.rt.Spec(h.ctr(web, 4)); spec.HostPort != 20000 {
		t.Fatalf("release 4 got port %d; release 1's port should be free after recovery", spec.HostPort)
	}
}

// Stopping the daemon never stops app containers or waits on them: an
// in-flight deploy is abandoned and failed by recovery on the next start.
func TestShutdownLeavesContainersAlone(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	defer h.rt.HoldHealthChecks()()
	rel, err := h.c.DeployImage(h.ctx, System, web, apitypes.DeployImageRequest{Image: "app:2"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, rel.Number, apitypes.ReleaseHealthChecking)
	before := len(h.rt.Ops())

	h.c.Close()
	for _, op := range h.rt.Ops()[before:] {
		if strings.HasPrefix(op, "stop ") || strings.HasPrefix(op, "remove ") {
			t.Fatalf("shutdown touched a container: %v", h.rt.Ops()[before:])
		}
	}
	if s := h.status(web, rel.Number); s != apitypes.ReleaseHealthChecking {
		t.Fatalf("abandoned release is %s, want it left for recovery", s)
	}

	h.c = h.start()
	if s := h.status(web, rel.Number); s != apitypes.ReleaseFailed {
		t.Fatalf("after the next start release 2 is %s", s)
	}
	h.waitRunning(h.ctr(web, 1))
}

// A stateful deploy that crashed after starting the new container but
// before saving its ID must not leave two containers on one volume.
func TestRecoveryFindsContainersByLabel(t *testing.T) {
	h := newHarness(t)
	db := h.service("db", apitypes.CreateServiceRequest{})
	if _, err := h.c.AddVolume(h.ctx, System, db, apitypes.AddVolumeRequest{Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	h.deploy(db, "postgres:17")
	defer h.rt.HoldHealthChecks()()
	rel, err := h.c.DeployImage(h.ctx, System, db, apitypes.DeployImageRequest{Image: "postgres:18"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(db, rel.Number, apitypes.ReleaseHealthChecking)
	if err := h.store.SetReleaseContainer(h.ctx, store.SetReleaseContainerParams{ContainerID: "", ID: rel.ID}); err != nil {
		t.Fatal(err)
	}

	h.crash()
	if got := h.rt.Running(); !slices.Equal(got, []string{h.ctr(db, 1)}) {
		t.Fatalf("running = %v, want only release 1", got)
	}
}
