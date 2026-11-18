//go:build !dockcli

package cli

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/api"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/cpbackup"
)

// `dock admin recover` reads the plan from a daemon in recovery mode with
// the recovery file's token, a caller without it is refused, and --done
// ends the mode.
func TestAdminRecover(t *testing.T) {
	setup(t)
	var dir string
	ts := newTestServerWith(t, func(o *core.Options) {
		dir = o.Config.DataDir
		if err := cpbackup.MarkRecovering(dir, "a backup of old"); err != nil {
			t.Fatal(err)
		}
		rec, err := cpbackup.ReadRecovery(dir)
		if err != nil {
			t.Fatal(err)
		}
		o.Recovery = rec
	})
	internal := httptest.NewServer(api.Internal(ts.core, slog.New(slog.DiscardHandler), nil, http.NotFoundHandler(), http.NotFoundHandler(), false))
	t.Cleanup(internal.Close)
	cfg := writeConfig(t, dir, "internal_listen = \""+strings.TrimPrefix(internal.URL, "http://")+"\"\n")

	res, err := internal.Client().Get(internal.URL + api.RecoverPath + "plan")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("plan without the token: %s", res.Status)
	}

	code, stdout, stderr := exec(t, "admin", "recover", "--config", cfg)
	if code != 0 || !strings.Contains(stdout, "a backup of old") || !strings.Contains(stdout, "nothing to restore") {
		t.Fatalf("plan: exit %d: %q %s", code, stdout, stderr)
	}
	// --yes runs the plan and, with nothing failed, ends the mode.
	code, stdout, stderr = exec(t, "admin", "recover", "--config", cfg, "--yes")
	if code != 0 || !strings.Contains(stdout, "0 failed") || !strings.Contains(stdout, "Recovery mode ended") {
		t.Fatalf("run: exit %d: %q %s", code, stdout, stderr)
	}
	if ts.core.Recovering() {
		t.Fatal("still in recovery mode")
	}
	if code, _, _ := exec(t, "admin", "recover", "--config", cfg); code != int(output.ExitConflict) {
		t.Fatalf("recover outside recovery mode: exit %d", code)
	}
}
