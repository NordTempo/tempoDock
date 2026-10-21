package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// `dock discard` drops some variables, one service's changes, or all.
func TestDiscardSelection(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	appDir(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	at := []string{"-p", "shop", "-e", "production"}
	web := append([]string{"-s", "web"}, at...)
	exec(t, "project", "create", "shop")
	exec(t, append([]string{"service", "create", "web"}, at...)...)
	exec(t, append([]string{"env", "set", "A=1", "B=2"}, web...)...)
	exec(t, append([]string{"env", "set", "S=1", "--shared"}, at...)...)

	steps := []struct {
		args []string
		left int
	}{
		{append([]string{"discard", "A", "--yes"}, web...), 2},
		{append([]string{"discard", "--yes"}, web...), 1},
		{append([]string{"discard", "--yes"}, at...), 0},
	}
	for _, s := range steps {
		if p := ok[apitypes.PendingChanges](t, s.args...); p.Count != s.left {
			t.Fatalf("%v left %+v, want %d staged", s.args, p, s.left)
		}
	}
}

// `dock audit --service` shows one service's events; rollback names what
// it copies.
func TestAuditByServiceAndRollbackOutput(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	appDir(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	at := []string{"-p", "shop", "-e", "production"}
	exec(t, "project", "create", "shop")
	ok[apitypes.Release](t, append([]string{"deploy", "--image", "app:1", "-s", "web"}, at...)...)
	ok[apitypes.Release](t, append([]string{"deploy", "--image", "app:2", "-s", "web"}, at...)...)
	ok[apitypes.Release](t, append([]string{"deploy", "--image", "app:1", "-s", "worker"}, at...)...)

	events := ok[[]apitypes.AuditEvent](t, append([]string{"audit", "-s", "web"}, at...)...)
	if len(events) != 3 { // service.create and two deploys
		t.Fatalf("web's events = %+v", events)
	}
	for _, e := range events {
		if e.Target != "shop/production/web" {
			t.Errorf("event of another target: %+v", e)
		}
	}

	code, _, stderr := exec(t, append([]string{"rollback", "-s", "web"}, at...)...)
	if code != 0 || !strings.Contains(stderr, "release 3 copies release 1, image app:1") {
		t.Fatalf("rollback = %d\n%s", code, stderr)
	}
}
