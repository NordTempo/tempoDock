package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// --grep and --stderr reach the server, which sends only the matching lines.
func TestLogsFilter(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	if code, _, stderr := execStdin(t, srv.tokens[apitypes.ScopeAdmin]+"\n", "login", srv.url, "--token-stdin"); code != 0 {
		t.Fatalf("login: exit %d\n%s", code, stderr)
	}
	ok[apitypes.Project](t, "project", "create", "shop")
	target := []string{"-p", "shop", "-e", "production", "-s", "web"}
	if code, _, stderr := exec(t, append([]string{"deploy", "--image", "app:1"}, target...)...); code != 0 {
		t.Fatalf("deploy: exit %d\n%s", code, stderr)
	}
	ctr := srv.rt.Running()[0]
	srv.rt.AddLogs(ctr, "GET /api error 500")
	srv.rt.AddStderr(ctr, "error: db timeout", "warn: slow query")

	code, stdout, stderr := exec(t, append([]string{"logs", "--grep", "error", "--stderr"}, target...)...)
	if code != 0 || stdout != "error: db timeout\n" {
		t.Fatalf("logs --grep error --stderr: exit %d\n%q\n%s", code, stdout, stderr)
	}
	if code, _, stderr := exec(t, append([]string{"logs", "--build", "--grep", "x"}, target...)...); code != int(output.ExitUsage) || !strings.Contains(stderr, "grep") {
		t.Errorf("logs --build --grep: exit %d\n%s", code, stderr)
	}
}
