package core

import (
	"archive/tar"
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Without the platform registry a built image has only its build tag,
// the name it has on the server it was built on. When the image is gone
// from the server, a rollback to it fails and says to build again: the
// tag is never pulled by name, which Docker would read as a repository
// on Docker Hub.
func TestBuildTagIsNeverPulledByName(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	source := func() *bytes.Buffer {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: 13, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("FROM scratch\n"))
		_ = tw.Close()
		return &buf
	}
	for range 2 {
		rel, err := h.c.DeploySource(h.ctx, testSystem, web, source(), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseActive {
			t.Fatalf("release %d = %s: %s", r.Number, r.Status, r.Error)
		}
	}
	first, err := h.c.GetRelease(h.ctx, testSystem, web, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.ImageRef, buildTagPrefix) {
		t.Fatalf("release 1's image = %q, want a build tag", first.ImageRef)
	}
	// The image leaves the server: pruned, or the server was replaced.
	eventually(t, "release 1's container to be removed, so its image can go", func() bool {
		return h.rt.RemoveImage(h.ctx, first.ImageRef) == nil
	})

	rb, err := h.c.Rollback(h.ctx, testSystem, web, 1)
	if err != nil {
		t.Fatal(err)
	}
	r := h.wait(web, rb.Number)
	if r.Status != apitypes.ReleaseFailed || !strings.Contains(r.Error, "deploy the service again to build it") {
		t.Fatalf("the rollback: %s: %s", r.Status, r.Error)
	}
	if pull := "pull " + first.ImageRef; slices.Contains(h.rt.Ops(), pull) {
		t.Errorf("the rollback asked the server to %q", pull)
	}
	// The service keeps serving its live release.
	if live, err := h.c.GetRelease(h.ctx, testSystem, web, 2); err != nil || live.Status != apitypes.ReleaseActive {
		t.Fatalf("release 2 after the failed rollback: %+v, %v", live.Status, err)
	}
}

// A build tag isn't an image name anyone deploys: in every spelling
// Docker reads as the same name it is refused, for a deploy and for a
// service's image setting. Other names that only look alike pass.
func TestBuildTagsAreNotImageNames(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	for ref, refused := range map[string]bool{
		"dock/p1_e1_s1:1":                         true,
		"dock/p1_e1_s1":                           true,
		"docker.io/dock/p1_e1_s1:1":               true,
		"Docker.IO/dock/p9_e9_s9:3":               true,
		"index.docker.io/dock/p1_e1_s1@sha256:ab": true,
		"registry-1.docker.io/dock/p2_e3_s4:5":    true,
		"dock/app:1":                              false,
		"dock/p1_e1:1":                            false,
		"dockp/p1_e1_s1:1":                        false,
		"ghcr.io/dock/p1_e1_s1:1":                 false,
	} {
		rel, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: ref})
		if got := CodeOf(err) == apitypes.CodeValidation && strings.Contains(err.Error(), "a name Dock gives the images it builds"); got != refused {
			t.Errorf("deploying %q: refused as a build tag = %v, want %v (%v)", ref, got, refused, err)
		}
		if err == nil {
			h.wait(web, rel.Number)
		}
	}
	if _, err := h.c.CreateService(h.ctx, testSystem, EnvRef{"shop", "production"}, apitypes.CreateServiceRequest{Name: "api", Image: "dock/p1_e1_s1:1"}); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("a service with a build tag as its image: %v", err)
	}
}

// An image is the platform's own for a release of the project its name
// holds, and for no other: a name written into another project's release
// doesn't make the image that project's to remove or to read.
func TestOwnImageIsItsProjects(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		project int64
		ref     string
		own     bool
	}{
		{1, "dock/p1_e1_s1:1", true},
		{1, "dock/p1_e2_s9:4", true}, // another environment of the project: where a promotion comes from
		{2, "dock/p1_e1_s1:1", false},
		{1, "dock/p11_e1_s1:1", false},
		{1, "nginx:1.27", false},
		{1, "ghcr.io/dock/p1_e1_s1:1", false},
	} {
		if got := h.c.ownImage(tc.project, tc.ref); got != tc.own {
			t.Errorf("ownImage(%d, %q) = %v, want %v", tc.project, tc.ref, got, tc.own)
		}
	}
}
