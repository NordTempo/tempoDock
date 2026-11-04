package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
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
