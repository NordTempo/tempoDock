package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// An operator checks a server's host now and reads the results back,
// in JSON and as text.
func TestServerChecksCommand(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	if code, _, stderr := execStdin(t, srv.tokens[apitypes.ScopeAdmin]+"\n", "login", srv.url, "--token-stdin"); code != 0 {
		t.Fatal(stderr)
	}
	servers := ok[[]apitypes.Server](t, "server", "list")
	if len(servers) == 0 {
		t.Fatal("no server")
	}
	name := servers[0].Name
	if v := ok[apitypes.HostChecks](t, "server", "checks", name); v.CheckedAt != nil && len(v.Checks) == 0 {
		t.Fatalf("before = %+v", v)
	}
	v := ok[apitypes.HostChecks](t, "server", "checks", name, "--now")
	if v.CheckedAt == nil || len(v.Checks) == 0 || v.Server != name {
		t.Fatalf("checked = %+v", v)
	}
	if code, stdout, stderr := exec(t, "server", "checks", name); code != 0 || !strings.Contains(stdout, "SSH takes keys only") {
		t.Fatalf("exit %d: %s %s", code, stdout, stderr)
	}
	if code, _, stderr := exec(t, "server", "checks", "dismiss", name, "docker_tcp", "--reason", "x"); code == 0 || !strings.Contains(stderr, "no finding") {
		t.Fatalf("dismissing a check that passed: exit %d %s", code, stderr)
	}
}
