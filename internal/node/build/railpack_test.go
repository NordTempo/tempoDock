package build

import (
	"testing"

	"github.com/distribution/reference"
	"github.com/railwayapp/railpack/core/generate"
	"github.com/railwayapp/railpack/core/plan"
)

// A planner that hits its memory cap crashes in one of several ways,
// depending on the kernel and machine; all mean the same to users. A
// planner error (a panic in its own code) is not a runtime crash.
func TestRuntimeCrash(t *testing.T) {
	for stderr, want := range map[string]bool{
		"fatal error: runtime: out of memory\n\ngoroutine 1":            true,
		"fatal error: runtime: cannot allocate memory\n\ngoroutine 1":   true,
		"SIGSEGV: segmentation violation\nPC=0x43b47c m=3 sigcode=1":    true,
		"panic: open /src/package.json: no such file or directory\n...": false,
	} {
		if got := runtimeCrash(stderr); got != want {
			t.Errorf("runtimeCrash(%q) = %v", stderr, got)
		}
	}
}

// Every image a Railpack build of this version names is pinned, so a
// Railpack upgrade that names new ones fails here until railpackImages
// has their digests.
func TestRailpackImagesPinned(t *testing.T) {
	for _, ref := range []string{railpackFrontend(), generate.RailpackBuilderImage, plan.RailpackRuntimeImage, "alpine:latest"} {
		named, err := reference.ParseNormalizedNamed(ref)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := railpackImages[named.String()]; !ok {
			t.Errorf("%s is not pinned in railpackImages", named)
		}
	}
}
