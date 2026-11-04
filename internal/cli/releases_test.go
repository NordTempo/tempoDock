package cli

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/node"
)

// waitUntil polls cond every few milliseconds for up to 5 seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// TestReleaseWait: `dock release wait` follows a detached deploy like
// `dock deploy` would, and exits by its outcome; a failed release shows the
// end of its logs, and hints name the full target.
func TestReleaseWait(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	appDir(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	exec(t, "project", "create", "shop")
	web := []string{"-p", "shop", "-e", "production", "-s", "web"}
	ok[apitypes.Release](t, append([]string{"deploy"}, web...)...)

	// A detached deploy names the command that follows it.
	hold := srv.rt.HoldHealthChecks()
	code, _, stderr := exec(t, append([]string{"deploy", "--detach"}, web...)...)
	if code != 0 || !strings.Contains(stderr, "`dock release wait 2 -p shop -e production -s web`") {
		t.Fatalf("deploy --detach: exit %d\n%s", code, stderr)
	}

	// Another deploy meanwhile is refused, with how to wait for this one.
	code, _, stderr = exec(t, append([]string{"deploy"}, web...)...)
	if code != int(output.ExitConflict) || !strings.Contains(stderr, "Wait for it with `dock release wait 2 -p shop -e production -s web`") {
		t.Fatalf("deploy during a deploy: exit %d\n%s", code, stderr)
	}

	// Giving up after --timeout says how to wait again.
	code, _, stderr = exec(t, append([]string{"release", "wait", "--timeout", "50ms"}, web...)...)
	if code != int(output.ExitError) || !strings.Contains(stderr, "still health_checking") || !strings.Contains(stderr, "`dock release wait 2 -p shop -e production -s web`") {
		t.Fatalf("wait --timeout: exit %d\n%s", code, stderr)
	}

	// Without a number it follows the newest release until it is live.
	var out, errOut syncBuffer
	done := make(chan int)
	go func() {
		done <- run(context.Background(), append([]string{"release", "wait"}, web...), &output.IO{In: strings.NewReader(""), Out: &out, Err: &errOut})
	}()
	waitUntil(t, "wait to follow", func() bool { return strings.Contains(errOut.String(), "Following release 2") })
	hold()
	if code := <-done; code != 0 || !strings.Contains(out.String(), "Release 2 of web is live") {
		t.Fatalf("wait: exit %d\n%s%s", code, out.String(), errOut.String())
	}

	// A failed health check: the container's output comes with the release.
	hold = srv.rt.HoldHealthChecks()
	exec(t, append([]string{"deploy", "--detach"}, web...)...)
	var ctr string
	waitUntil(t, "release 3's container", func() bool {
		i := slices.IndexFunc(srv.rt.Running(), func(n string) bool { return strings.HasSuffix(n, "_r3") })
		if i >= 0 {
			ctr = srv.rt.Running()[i]
		}
		return i >= 0
	})
	srv.rt.AddLogs(ctr, "panic: DATABASE_URL is not set")
	srv.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	hold()
	code, stdout, stderr := exec(t, append([]string{"release", "wait", "3", "--json"}, web...)...)
	var failed releaseView
	_ = json.Unmarshal([]byte(stdout), &failed)
	if code != int(output.ExitDeployFailed) || failed.Status != apitypes.ReleaseFailed || failed.Logs == nil ||
		len(failed.Logs.Container) != 1 || !strings.HasSuffix(failed.Logs.Container[0], " panic: DATABASE_URL is not set") ||
		!strings.Contains(stderr, "`dock release show 3 -p shop -e production -s web`") {
		t.Fatalf("wait for a failed release: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}

	// release show carries the end of both logs, unless --log-lines 0.
	shown := ok[releaseView](t, append([]string{"release", "show", "3", "--log-lines", "1"}, web...)...)
	if shown.Logs == nil || len(shown.Logs.Container) != 1 || len(shown.Logs.Build) != 1 {
		t.Fatalf("release show logs = %+v", shown.Logs)
	}
	if shown := ok[releaseView](t, append([]string{"release", "show", "3", "--log-lines", "0"}, web...)...); shown.Logs != nil {
		t.Errorf("release show --log-lines 0: logs = %+v", shown.Logs)
	}
	if _, stdout, _ := exec(t, append([]string{"release", "show", "3"}, web...)...); !strings.Contains(stdout, "Output (last 1 line):") {
		t.Errorf("release show:\n%s", stdout)
	}
}
