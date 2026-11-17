package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/node"
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
	// A first deploy that fails exits deploy_failed, like `dock deploy`.
	srv.rt.FailHealthCheck(func(s node.ContainerSpec) error {
		if s.Env["APP_SECRET"] != "" { // umami, not its database
			return errors.New("connection refused")
		}
		return nil
	})
	if code, _, stderr := exec(t, append([]string{"template", "use", "umami", "--name", "broken"}, env...)...); code != int(output.ExitDeployFailed) || !strings.Contains(stderr, "dock diagnose") {
		t.Fatalf("failed first deploy: exit %d\n%s", code, stderr)
	}
}

// `dock template save`: an environment's services become the org's
// template, which lists, prints as a file, saves back from it and goes.
func TestTemplateSaveCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	env := []string{"-p", "shop", "-e", "production"}
	if code, _, stderr := exec(t, append([]string{"deploy", "-s", "web", "--image", "web:1"}, env...)...); code != 0 {
		t.Fatal(stderr)
	}
	if code, stdout, stderr := exec(t, append([]string{"template", "save", "web-stack"}, env...)...); code != 0 || !strings.Contains(stdout+stderr, "Saved web from production as the organization's template web-stack") {
		t.Fatalf("save: %d\n%s%s", code, stdout, stderr)
	}
	if code, stdout, _ := exec(t, "template", "list"); code != 0 || !strings.Contains(stdout, "organization's") {
		t.Fatalf("list: %d\n%s", code, stdout)
	}
	code, file, _ := exec(t, "template", "show", "web-stack", "--toml")
	if code != 0 || !strings.Contains(file, "name = 'web-stack'") {
		t.Fatalf("show --toml: %d\n%s", code, file)
	}
	path := filepath.Join(t.TempDir(), "t.toml")
	if err := os.WriteFile(path, []byte(strings.Replace(file, "name = 'web-stack'", "name = 'web-two'", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := exec(t, "template", "save", "--file", path); code != 0 {
		t.Fatalf("save --file: %s", stderr)
	}
	if code, _, stderr := exec(t, "template", "remove", "web-two"); code != 0 {
		t.Fatalf("remove: %s", stderr)
	}
	if code, _, _ := exec(t, "template", "show", "web-two"); code == 0 {
		t.Fatal("removed template still shows")
	}
}
