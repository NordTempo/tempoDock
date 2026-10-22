package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// Channels are added, listed without their URL, tested and removed from
// the CLI; a failing test exits 7. Deploy tokens can't see alerts.
func TestAlertChannelCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	srv.core.AllowLoopbackAlertChannels(true) // hook listens on 127.0.0.1
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/down") {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(hook.Close)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])

	created := ok[apitypes.CreatedAlertChannel](t, "alerts", "channel", "add", "ops", "--webhook", hook.URL+"/ok?token=s3cret", "--min-severity", "critical")
	if created.Secret == "" || created.Channel.MinSeverity != "critical" || created.Channel.RenotifyMinutes != 240 {
		t.Fatalf("created = %+v", created)
	}
	ok[apitypes.CreatedAlertChannel](t, "alerts", "channel", "add", "down", "--slack", hook.URL+"/down", "--renotify", "0")
	chs := ok[[]apitypes.AlertChannel](t, "alerts", "channel", "list")
	if len(chs) != 2 || chs[0].Name != "down" || chs[0].RenotifyMinutes != 0 || strings.Contains(chs[1].Target, "s3cret") {
		t.Fatalf("channels = %+v", chs)
	}
	if res := ok[apitypes.AlertChannelTest](t, "alerts", "channel", "test", "ops"); !res.OK {
		t.Fatalf("test = %+v", res)
	}
	if code, _, stderr := exec(t, "alerts", "channel", "test", "down"); code != int(output.ExitUnreachable) || !strings.Contains(stderr, "502") {
		t.Fatalf("failing test: exit %d %q", code, stderr)
	}
	if code, _, stderr := exec(t, "alerts", "channel", "remove", "down"); code != int(output.ExitUsage) || !strings.Contains(stderr, "--yes") {
		t.Fatalf("remove without --yes: exit %d %q", code, stderr)
	}
	exec(t, "alerts", "channel", "remove", "down", "--yes")
	if alerts := ok[[]apitypes.Alert](t, "alerts"); len(alerts) != 0 {
		t.Fatalf("alerts = %+v", alerts)
	}

	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	if code, _, _ := exec(t, "alerts"); code != int(output.ExitAuth) {
		t.Fatalf("deploy token listing alerts: exit %d", code)
	}
}
