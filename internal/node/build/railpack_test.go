package build

import "testing"

// A planner that hits its memory cap crashes with either wording,
// depending on the kernel and machine; both mean the same to users.
func TestOutOfMemory(t *testing.T) {
	for stderr, want := range map[string]bool{
		"fatal error: runtime: out of memory\n\ngoroutine 1":            true,
		"fatal error: runtime: cannot allocate memory\n\ngoroutine 1":   true,
		"panic: open /src/package.json: no such file or directory\n...": false,
	} {
		if got := outOfMemory(stderr); got != want {
			t.Errorf("outOfMemory(%q) = %v", stderr, got)
		}
	}
}
