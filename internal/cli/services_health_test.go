package cli

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// --health-check-interval takes a duration or "off"; the other ongoing
// check flags stage like any setting.
func TestServiceHealthCheckFlags(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	appDir(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	at := []string{"-p", "shop", "-e", "production"}
	exec(t, "project", "create", "shop")

	svc := ok[apitypes.Service](t, append([]string{"service", "create", "web", "--health-check-interval", "off"}, at...)...)
	if svc.HealthCheckIntervalSeconds != -1 {
		t.Fatalf("created with interval %d, want -1", svc.HealthCheckIntervalSeconds)
	}
	res := ok[apitypes.ChangeResult](t, append([]string{"service", "update", "web",
		"--health-check-interval", "45s", "--health-check-probe-timeout", "2s",
		"--health-check-retries", "5", "--health-check-start-period", "1m"}, at...)...)
	want := map[string]string{
		"health_check_interval_seconds": "45", "health_check_probe_timeout_seconds": "2",
		"health_check_retries": "5", "health_check_start_period_seconds": "60",
	}
	if len(res.Pending.Settings) != len(want) {
		t.Fatalf("staged: %+v", res.Pending.Settings)
	}
	for _, ch := range res.Pending.Settings {
		if want[ch.Field] != ch.New {
			t.Errorf("%s staged as %q, want %q", ch.Field, ch.New, want[ch.Field])
		}
	}
	if code, _, _ := exec(t, append([]string{"service", "update", "web", "--health-check-interval", "fast"}, at...)...); code == 0 {
		t.Fatal("a bad interval must be refused")
	}
}
