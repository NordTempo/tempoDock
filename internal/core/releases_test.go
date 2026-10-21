package core

import (
	"archive/tar"
	"bytes"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The image setting follows deploys: an image deploy sets it, so deploying
// the setting again redeploys that image, and a source deploy clears it.
func TestImageSettingFollowsDeploys(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	image := func() string {
		t.Helper()
		s, err := h.c.GetService(h.ctx, System, web)
		if err != nil {
			t.Fatal(err)
		}
		return s.Image
	}

	h.deploy(web, "app:2")
	if got := image(); got != "app:2" {
		t.Fatalf("image after deploying app:2 = %q", got)
	}
	if r := h.deploy(web, ""); r.ImageRef != "app:2" || r.Status != apitypes.ReleaseActive {
		t.Fatalf("redeploying the setting: %+v", r)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: 11, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("FROM app:2\n"))
	_ = tw.Close()
	rel, err := h.c.DeploySource(h.ctx, System, web, &buf, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	h.wait(web, rel.Number)
	if got := image(); got != "" {
		t.Fatalf("image after a source deploy = %q, want none", got)
	}
}
