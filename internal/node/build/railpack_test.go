package build

import "testing"

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
