package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

func TestAnalyticsCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])

	if st := ok[apitypes.AnalyticsStatus](t, "analytics"); st.Enabled {
		t.Fatalf("analytics is on before anyone turned it on: %+v", st)
	}
	code, stdout, stderr := exec(t, "analytics", "server")
	if code != 0 || !strings.Contains(stdout, "The analytics store runs on") || !strings.Contains(stdout, "Analytics  on") {
		t.Fatalf("analytics server = %d\n%s\n%s", code, stdout, stderr)
	}
	// The store starts in the background.
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := ok[apitypes.AnalyticsStatus](t, "analytics")
		if st.StoreRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the store never answered: %+v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, _, stderr := exec(t, "analytics", "server", "nope"); code == 0 || !strings.Contains(stderr, "dock server list") {
		t.Errorf("an unknown server: %d %s", code, stderr)
	}
}
