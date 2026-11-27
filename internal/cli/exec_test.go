package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// TestExec: `dock exec` streams stdin and output through the running
// container, passes the exit code on, and needs admin.
func TestExec(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	at := []string{"-p", "shop", "-e", "production"}
	ok[apitypes.Release](t, append([]string{"deploy", "-s", "web", "--image", "web:1"}, at...)...)

	// The fake runtime echoes a session's stdin, like cat, and prints a
	// command without it.
	if code, stdout, stderr := execStdin(t, "hello", append(append([]string{"exec", "-i", "web"}, at...), "--", "cat")...); code != 0 || stdout != "hello" {
		t.Fatalf("exec -i cat: exit %d, stdout %q\n%s", code, stdout, stderr)
	}
	if code, stdout, stderr := exec(t, append(append([]string{"exec", "web"}, at...), "--", "ls", "-la", "/my dir")...); code != 0 || stdout != "ls -la /my dir\n" {
		t.Fatalf("exec ls: exit %d, stdout %q\n%s", code, stdout, stderr)
	}
	if code, _, stderr := exec(t, append(append([]string{"exec", "web"}, at...), "--", "exit", "3")...); code != 3 || stderr != "" {
		t.Fatalf("exec exit 3: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := exec(t, append([]string{"exec", "web", "ls"}, at...)...); code != int(output.ExitUsage) || !strings.Contains(stderr, "dock exec web -- ls") {
		t.Fatalf("exec without --: exit %d, stderr %q", code, stderr)
	}

	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	if code, _, stderr := exec(t, append(append([]string{"exec", "web"}, at...), "--", "ls")...); code != int(output.ExitAuth) {
		t.Fatalf("exec with deploy: exit %d, stderr %q", code, stderr)
	}
}
