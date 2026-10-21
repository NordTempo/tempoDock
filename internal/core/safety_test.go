package core

import (
	"errors"
	"io"
	"os"
	"path/filepath"
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

// Uploads are claimed before they stream to disk: one per service, and a
// small cap per server, so parallel uploads can't fill the disk.
func TestConcurrentUploadsAreLimited(t *testing.T) {
	h := newHarness(t)
	refs := []ServiceRef{h.service("a", apitypes.CreateServiceRequest{}), h.service("b", apitypes.CreateServiceRequest{}), h.service("c", apitypes.CreateServiceRequest{})}
	uploading := func() int {
		h.c.uploadsMu.Lock()
		defer h.c.uploadsMu.Unlock()
		return len(h.c.uploads)
	}
	errs := make(chan error, maxConcurrentUploads)
	var clients []*io.PipeWriter
	for _, ref := range refs[:maxConcurrentUploads] {
		pr, pw := io.Pipe()
		clients = append(clients, pw)
		go func() {
			_, err := h.c.DeploySource(h.ctx, System, ref, pr, 1<<20)
			errs <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for uploading() < maxConcurrentUploads {
		if time.Now().After(deadline) {
			t.Fatal("uploads never started")
		}
		time.Sleep(2 * time.Millisecond)
	}

	if _, err := h.c.DeploySource(h.ctx, System, refs[0], strings.NewReader("x"), 1<<20); CodeOf(err) != apitypes.CodeDeployInProgress {
		t.Fatalf("second upload to one service: %v", err)
	}
	if _, err := h.c.DeploySource(h.ctx, System, refs[2], strings.NewReader("x"), 1<<20); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("upload over the server cap: %v", err)
	}
	for _, pw := range clients {
		_ = pw.CloseWithError(errors.New("client went away"))
	}
	for range clients {
		<-errs
	}
	if n := uploading(); n != 0 {
		t.Fatalf("%d uploads still claimed after they ended", n)
	}
}

// Uploads and build contexts left by a crash are removed on the next
// start; build logs stay.
func TestStartClearsTempFiles(t *testing.T) {
	h := newHarness(t)
	dir := h.opts.Config.DataDir
	leftovers := []string{filepath.Join(dir, "uploads", "abc.tar"), filepath.Join(dir, "builds", "work", "build-123", "main.go")}
	keep := filepath.Join(dir, "builds", "1.log")
	for _, p := range append(leftovers, keep) {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.restart()
	for _, p := range leftovers {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the start: %v", p, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("build log removed: %v", err)
	}
}

func TestHostnameValidation(t *testing.T) {
	for host, ok := range map[string]bool{
		"app.example.com": true, "Shop.Example.COM.": true, "x.co": true, "1password.com": true,
		"203.0.113.10": false, "[::1]": false, "::1": false, "10.0.0.1": false, "1.2.3.4.5": false,
		"localhost": false, "app.localhost": false, "intranet": false, "foo.123": false,
	} {
		if _, err := normalizeHostname(host); (err == nil) != ok {
			t.Errorf("normalizeHostname(%q) err = %v, want ok %v", host, err, ok)
		}
	}
}

func TestImageRefValidation(t *testing.T) {
	for ref, ok := range map[string]bool{
		"nginx": true, "nginx:1.27": true, "library/nginx": true, "ghcr.io/acme/app:v1": true,
		"registry.example.com:5000/app": true, "docker.io/traefik/whoami@sha256:abc": true, "8.8.8.8/app": true,
		"localhost/app": false, "localhost:5000/app": false, "registry.localhost/app": false,
		"127.0.0.1:5000/app": false, "10.1.2.3/app": false, "192.168.1.5:5000/app": false, "172.16.0.1/app": false,
		"169.254.169.254/app": false, "[::1]:5000/app": false, "[fd00::1]/app": false, "0.0.0.0/app": false,
	} {
		if err := validateImageRef(ref); (err == nil) != ok {
			t.Errorf("validateImageRef(%q) err = %v, want ok %v", ref, err, ok)
		}
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
