package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// The heartbeat is set from stdin, shown without its URL, tested and
// removed from the CLI; a failing test exits 7 and none set exits 4.
func TestHeartbeatCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	srv.core.AllowLoopbackAlertChannels(true) // the receiver listens on 127.0.0.1
	var down atomic.Bool
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(hook.Close)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])

	if code, _, stderr := exec(t, "alerts", "heartbeat"); code != int(output.ExitNotFound) || !strings.Contains(stderr, "dock alerts heartbeat set") {
		t.Fatalf("showing no heartbeat: exit %d, %s", code, stderr)
	}
	if code, _, stderr := exec(t, "alerts", "heartbeat", "set", hook.URL, "--every", "90"); code != int(output.ExitUsage) {
		t.Fatalf("--every 90: exit %d, %s", code, stderr)
	}
	code, stdout, stderr := execStdin(t, hook.URL+"/ping/s3cret\n", "alerts", "heartbeat", "set", "-", "--every", "2")
	if code != 0 || !strings.Contains(stderr+stdout, hook.URL+" every 2 min") || strings.Contains(stdout+stderr, "s3cret") {
		t.Fatalf("set: exit %d, %s%s", code, stdout, stderr)
	}
	if res := ok[apitypes.HeartbeatTest](t, "alerts", "heartbeat", "test"); !res.OK || res.Heartbeat.LastSentAt == nil {
		t.Fatalf("test = %+v", res)
	}
	down.Store(true)
	if code, _, stderr := exec(t, "alerts", "heartbeat", "test"); code != int(output.ExitUnreachable) || !strings.Contains(stderr, "502") || strings.Contains(stderr, "s3cret") {
		t.Fatalf("failing test: exit %d, %s", code, stderr)
	}
	if code, stdout, _ := exec(t, "alerts", "heartbeat"); code != 0 || !strings.Contains(stdout, hook.URL) || !strings.Contains(stdout, "502") || strings.Contains(stdout, "s3cret") {
		t.Fatalf("show: exit %d, %s", code, stdout)
	}
	if code, _, stderr := exec(t, "alerts", "heartbeat", "remove"); code != int(output.ExitUsage) || !strings.Contains(stderr, "--yes") {
		t.Fatalf("remove without --yes: exit %d, %s", code, stderr)
	}
	exec(t, "alerts", "heartbeat", "remove", "--yes")
	if code, _, _ := exec(t, "alerts", "heartbeat"); code != int(output.ExitNotFound) {
		t.Fatalf("showing a removed heartbeat: exit %d", code)
	}
}
