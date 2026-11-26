package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// A prune target that doesn't exist is a usage error that lists the
// ones that do, before anything is asked of a server.
func TestServerPruneUnknownTarget(t *testing.T) {
	code, _, stderr := exec(t, "server", "prune", "prod-1", "--only", "scanner,volumes", "--yes")
	if code != int(output.ExitUsage) || !strings.Contains(stderr, `"volumes"`) || !strings.Contains(stderr, "scan-db") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

// An operator lists the features and switches analytics and scanning off
// and on; the list says which are on.
func TestFeaturesCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	if code, _, stderr := execStdin(t, srv.tokens[apitypes.ScopeAdmin]+"\n", "login", srv.url, "--token-stdin"); code != 0 {
		t.Fatal(stderr)
	}
	ok[apitypes.AnalyticsStatus](t, "analytics", "on")
	ok[apitypes.ScanningStatus](t, "scanning", "off", "--remove-database")
	state := func() map[apitypes.FeatureKey]bool {
		out := map[apitypes.FeatureKey]bool{}
		for _, f := range ok[apitypes.Features](t, "features").Items {
			out[f.Key] = f.Enabled
		}
		return out
	}
	if s := state(); !s[apitypes.FeatureAnalytics] || s[apitypes.FeatureScanning] || s[apitypes.FeatureTracing] {
		t.Fatalf("features = %v", s)
	}
	if st := ok[apitypes.AnalyticsStatus](t, "analytics", "off"); st.Enabled {
		t.Fatalf("analytics off = %+v", st)
	}
	if s := state(); s[apitypes.FeatureAnalytics] {
		t.Fatalf("features after analytics off = %v", s)
	}
	if code, stdout, stderr := exec(t, "features"); code != 0 || !strings.Contains(stdout, "dock analytics on|off") {
		t.Fatalf("features table: exit %d %s %s", code, stdout, stderr)
	}
}
