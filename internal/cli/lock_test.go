package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/core"
)

// TestEnvironmentLock: `dock environment lock` locks, `status` and
// `environment list` show it, and a change in it exits 5 with
// environment_locked and a hint to ask an admin.
func TestEnvironmentLock(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")

	e := ok[apitypes.Environment](t, "environment", "lock", "production", "-p", "shop", "--reason", "live")
	if e.Lock == nil || e.Lock.Reason != "live" {
		t.Fatalf("lock: %+v", e)
	}
	if _, out, _ := exec(t, "status", "-p", "shop", "-e", "production"); !strings.Contains(out, "locked by admin (live)") {
		t.Fatalf("status: %s", out)
	}
	if _, out, _ := exec(t, "environment", "list", "-p", "shop"); !strings.Contains(out, "locked by admin") {
		t.Fatalf("list: %s", out)
	}

	code, _, stderr := exec(t, "env", "set", "A=1", "--shared", "-p", "shop", "-e", "production", "--json")
	var out struct {
		Error struct{ Code, Message, Hint string }
	}
	_ = json.Unmarshal([]byte(stderr), &out)
	if code != int(output.ExitConflict) || out.Error.Code != string(apitypes.CodeEnvironmentLocked) ||
		!strings.Contains(out.Error.Message, "shop/production is locked") || !strings.Contains(out.Error.Hint, "admin") {
		t.Fatalf("locked change: exit %d %s", code, stderr)
	}
	// The CLI runs with a token: unlocking is a change like any other.
	if code, _, stderr := exec(t, "env", "unlock", "-p", "shop", "-e", "production", "--for", "30m"); code != int(output.ExitConflict) {
		t.Fatalf("unlock: exit %d %s", code, stderr)
	}
}

// TestRequestCommands: a refused change says which request it filed, with
// --reason attached; `dock request list` and `wait` follow it to its end.
func TestRequestCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	exec(t, "environment", "lock", "production", "-p", "shop")
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])

	code, _, stderr := exec(t, "env", "set", "A=1", "--shared", "-p", "shop", "-e", "production", "--reason", "new flag")
	if code != int(output.ExitConflict) || !strings.Contains(stderr, "access request #1") || !strings.Contains(stderr, "dock request wait 1") {
		t.Fatalf("refused: exit %d %s", code, stderr)
	}
	list := ok[[]apitypes.AccessRequest](t, "request", "list")
	if len(list) != 1 || list[0].Reason != "new flag" || list[0].Status != apitypes.RequestPending {
		t.Fatalf("list: %+v", list)
	}
	if code, _, _ := exec(t, "request", "wait", "1", "--timeout", "1s"); code != int(output.ExitConflict) {
		t.Fatalf("wait while pending: exit %d", code)
	}
	admin := core.Actor{Kind: "user", OrgID: 1, Name: "saile@example.com", UserID: 1, Scope: apitypes.ScopeAdmin}
	if _, err := srv.core.DecideRequest(context.Background(), admin, 1, true, apitypes.DecideAccessRequest{}); err != nil {
		t.Fatal(err)
	}
	if r := ok[apitypes.AccessRequest](t, "request", "wait", "1"); r.Status != apitypes.RequestDone || r.DecidedBy != "saile@example.com" {
		t.Fatalf("wait: %+v", r)
	}
	if _, out, _ := exec(t, "request", "show", "#1"); !strings.Contains(out, "approved") && !strings.Contains(out, "done") {
		t.Fatalf("show: %s", out)
	}
}
