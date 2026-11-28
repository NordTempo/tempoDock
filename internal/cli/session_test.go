package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// TestPlainHTTPWarning: a remote plain http server gets one warning on
// stderr, with --json too; https and loopback get none. The .invalid
// hosts never resolve, so the commands fail without a server.
func TestPlainHTTPWarning(t *testing.T) {
	for server, warns := range map[string]bool{
		"http://dock.invalid:7070": true,
		"https://dock.invalid":     false,
		"http://127.0.0.1:1":       false,
		"http://localhost:1":       false,
	} {
		setup(t)
		t.Setenv(clientconfig.EnvURL, server)
		t.Setenv(clientconfig.EnvToken, "t")
		_, _, stderr := exec(t, "project", "list", "--json")
		if got := strings.Count(stderr, "is plain http"); got != map[bool]int{true: 1}[warns] {
			t.Errorf("%s: %d warnings in %q", server, got, stderr)
		}
		if warns && !strings.Contains(stderr, "\n{\"error\"") {
			t.Errorf("%s: no JSON error after the warning: %q", server, stderr)
		}
	}
}
