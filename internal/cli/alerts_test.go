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
// the CLI; a failing test exits 7.
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
	// --platform reaches the platform's channels, apart from the org's.
	ok[apitypes.CreatedAlertChannel](t, "alerts", "channel", "add", "pager", "--platform", "--slack", hook.URL+"/pager")
	if chs := ok[[]apitypes.AlertChannel](t, "alerts", "channel", "list", "--platform"); len(chs) != 1 || chs[0].Name != "pager" {
		t.Fatalf("platform channels = %+v", chs)
	}
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
	// Without Dock's email set up, an email channel says so when added,
	// and its failed test names the fix, which isn't the channel's.
	if code, _, stderr := exec(t, "alerts", "channel", "add", "mail", "--email", "ops@example.com"); code != 0 || !strings.Contains(stderr, "Email isn't set up") {
		t.Fatalf("email channel add: exit %d %q", code, stderr)
	}
	if code, _, stderr := exec(t, "alerts", "channel", "test", "mail"); code != int(output.ExitUnreachable) || !strings.Contains(stderr, "dock email set") || strings.Contains(stderr, "add it again") {
		t.Fatalf("email channel test: exit %d %q", code, stderr)
	}
	exec(t, "alerts", "channel", "remove", "mail", "--yes")
	if code, _, stderr := exec(t, "alerts", "channel", "remove", "down"); code != int(output.ExitUsage) || !strings.Contains(stderr, "--yes") {
		t.Fatalf("remove without --yes: exit %d %q", code, stderr)
	}
	exec(t, "alerts", "channel", "remove", "down", "--yes")
	if alerts := ok[[]apitypes.Alert](t, "alerts"); len(alerts) != 0 {
		t.Fatalf("alerts = %+v", alerts)
	}
}
