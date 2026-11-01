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

func TestAnalyticsSiteCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	exec(t, "project", "create", "shop")
	for _, args := range [][]string{
		{"deploy", "-p", "shop", "-e", "production", "-s", "web", "--image", "web:1"},
		{"domain", "add", "shop.example.com", "-p", "shop", "-e", "production", "-s", "web", "--detach"},
	} {
		if code, _, stderr := exec(t, args...); code != 0 {
			t.Fatalf("%v: %s", args, stderr)
		}
	}
	code, stdout, stderr := exec(t, "analytics", "enable", "shop.example.com")
	if code != 0 || !strings.Contains(stdout, "shop.example.com counts for site shop.example.com") || !strings.Contains(stderr, `<script src="/_dock/a/s.js" defer></script>`) {
		t.Fatalf("enable = %d\n%s\n%s", code, stdout, stderr)
	}
	if sites := ok[[]apitypes.Site](t, "analytics", "sites", "-p", "shop"); len(sites) != 1 || sites[0].Hostnames[0].Service != "web" {
		t.Fatalf("sites = %+v", sites)
	}
	site := ok[apitypes.Site](t, "analytics", "site", "set", "shop.example.com", "-p", "shop", "--exclude-path", "/admin/*", "--replay", "on", "--replay-sample", "10")
	if s := site.Settings; !*s.Replay || *s.ReplaySampleRate != 10 || len(*s.ExcludedPaths) != 1 || site.Overrides.MaskAllText != nil {
		t.Fatalf("settings = %+v", s)
	}
	site = ok[apitypes.Site](t, "analytics", "site", "set", "shop.example.com", "-p", "shop", "--reset", "replay_sample_rate")
	if *site.Settings.ReplaySampleRate != 20 || site.Overrides.ReplaySampleRate != nil {
		t.Errorf("after the reset: %+v", site.Settings)
	}
	if code, _, stderr := exec(t, "analytics", "site", "set", "shop.example.com", "-p", "shop", "--replay", "maybe"); code == 0 || !strings.Contains(stderr, "on or off") {
		t.Errorf("--replay maybe: %d %s", code, stderr)
	}
	if st := ok[apitypes.SiteStats](t, "analytics", "stats", "shop.example.com", "-p", "shop", "--range", "7d", "--filter", "country:is:NO"); st.Site != "shop.example.com" {
		t.Errorf("stats = %+v", st)
	}
	if e := ok[apitypes.SiteErrors](t, "analytics", "errors", "shop.example.com", "-p", "shop", "--range", "24h"); e.Site != "shop.example.com" || !e.Collecting || e.Bucket != "hour" {
		t.Errorf("errors = %+v", e)
	}
	if v := ok[apitypes.SiteVitals](t, "analytics", "vitals", "shop.example.com", "-p", "shop", "--by", "device"); len(v.Breakdowns) != 0 || v.Thresholds["inp"].Poor != 500 {
		t.Errorf("vitals = %+v", v)
	}
	if r := ok[apitypes.SiteReplays](t, "analytics", "replays", "shop.example.com", "-p", "shop", "--errors"); r.Site != "shop.example.com" || !r.Recording {
		t.Errorf("replays = %+v", r)
	}
	if code, _, stderr := exec(t, "analytics", "vitals", "shop.example.com", "-p", "shop", "--by", "referrer"); code == 0 || !strings.Contains(stderr, "break down by page") {
		t.Errorf("vitals by referrer: %d %s", code, stderr)
	}
	if code, _, stderr := exec(t, "analytics", "stats", "shop.example.com", "-p", "shop", "--filter", "nope"); code == 0 || !strings.Contains(stderr, "country:is:NO") {
		t.Errorf("a malformed filter: %d %s", code, stderr)
	}
	if code, _, stderr := exec(t, "analytics", "disable", "shop.example.com"); code != 0 {
		t.Fatalf("disable: %s", stderr)
	}
	if sites := ok[[]apitypes.Site](t, "analytics", "sites", "-p", "shop"); len(sites) != 1 || !sites[0].Archived {
		t.Errorf("after disable: %+v", sites)
	}
}
