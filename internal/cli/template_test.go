package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// `dock template`: list and show the catalog, plan and print a file
// without creating anything, then use one and wait for its deploys.
func TestTemplateCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	exec(t, "project", "create", "shop")

	if code, stdout, _ := exec(t, "template", "list", "--category", "databases"); code != 0 || !strings.Contains(stdout, "postgres") || strings.Contains(stdout, "umami") {
		t.Fatalf("list: %d\n%s", code, stdout)
	}
	if code, stdout, _ := exec(t, "template", "show", "grafana"); code != 0 || !strings.Contains(stdout, "GF_SECURITY_ADMIN_PASSWORD") {
		t.Fatalf("show: %d\n%s", code, stdout)
	}
	env := []string{"-p", "shop", "-e", "production"}
	if code, stdout, _ := exec(t, append([]string{"template", "use", "umami", "--plan"}, env...)...); code != 0 || !strings.Contains(stdout, "production/umami-db") {
		t.Fatalf("plan: %d\n%s", code, stdout)
	}
	if code, stdout, _ := exec(t, append([]string{"template", "use", "umami", "--file"}, env...)...); code != 0 || !strings.Contains(stdout, `name = 'umami-db'`) {
		t.Fatalf("file: %d\n%s", code, stdout)
	}
	res := ok[apitypes.UseTemplateResult](t, append([]string{"template", "use", "umami", "--name", "stats"}, env...)...)
	if strings.Join(res.Services, ",") != "stats,stats-db" || len(res.Releases) != 2 {
		t.Fatalf("use: %+v", res)
	}
	for _, r := range res.Releases {
		if r.Status != apitypes.ReleaseActive {
			t.Fatalf("%s release %d: %s", r.Service, r.Number, r.Status)
		}
	}
	if code, stdout, _ := exec(t, "service", "show", "stats", "-p", "shop", "-e", "production"); code != 0 || !strings.Contains(stdout, "umami, revision") {
		t.Fatalf("show: %d\n%s", code, stdout)
	}
	if code, _, stderr := exec(t, append([]string{"template", "use", "grafana", "--set", "nope"}, env...)...); code != 2 || !strings.Contains(stderr, "KEY=VALUE") {
		t.Fatalf("bad --set: %d %s", code, stderr)
	}
}
