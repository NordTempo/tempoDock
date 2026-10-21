package core

import (
	"testing"

	"github.com/NordTempo/platform/internal/apitypes"
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
