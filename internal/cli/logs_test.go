package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
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

// `dock logs -f` goes on to the release a deploy makes live, as the
// Logs tab does, and says why it ends when the service stops.
func TestLogsFollowsTheNextRelease(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	if code, _, stderr := execStdin(t, srv.tokens[apitypes.ScopeAdmin]+"\n", "login", srv.url, "--token-stdin"); code != 0 {
		t.Fatalf("login: exit %d\n%s", code, stderr)
	}
	ok[apitypes.Project](t, "project", "create", "shop")
	target := []string{"-p", "shop", "-e", "production", "-s", "web"}
	deploy := func(image string) {
		t.Helper()
		if code, _, stderr := exec(t, append([]string{"deploy", "--image", image}, target...)...); code != 0 {
			t.Fatalf("deploy %s: exit %d\n%s", image, code, stderr)
		}
	}
	deploy("app:1")
	srv.rt.AddLogs("dock_p1_e1_s1_r1", "one")

	out, w := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		code := run(context.Background(), append([]string{"logs", "-f"}, target...), &output.IO{In: strings.NewReader(""), Out: w, Err: &stderr})
		_ = w.Close()
		done <- code
	}()
	lines := bufio.NewScanner(out)
	next := func(want string) {
		t.Helper()
		if !lines.Scan() || lines.Text() != want {
			t.Fatalf("line %q, want %q", lines.Text(), want)
		}
	}
	next("one")
	deploy("app:2")
	srv.rt.AddLogs("dock_p1_e1_s1_r2", "two")
	next("two")
	if code, _, errs := exec(t, append([]string{"service", "stop", "--yes"}, target...)...); code != 0 {
		t.Fatalf("stop: exit %d\n%s", code, errs)
	}
	for lines.Scan() {
	}
	if code := <-done; code != 0 || !strings.Contains(stderr.String(), "release 2 of shop/production/web is live") || !strings.Contains(stderr.String(), "was stopped") {
		t.Fatalf("logs -f: exit %d\n%s", code, stderr.String())
	}
}
