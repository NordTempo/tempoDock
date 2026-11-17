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

// `dock apply FILE`: an export plans no change; an edited file plans as a
// diff (human and --json), stages without going live, and with --deploy
// applies and follows the releases. Without a file, --plan is a usage
// error.
func TestApplyProjectFile(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	exec(t, "project", "create", "shop")
	if code, _, stderr := exec(t, "deploy", "-p", "shop", "-e", "production", "-s", "web", "--image", "web:1"); code != 0 {
		t.Fatal(stderr)
	}
	dir := t.TempDir()
	_, export, _ := exec(t, "project", "export", "shop")
	exported := filepath.Join(dir, "dock.project.toml")
	if err := os.WriteFile(exported, []byte(export), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := exec(t, "apply", exported, "--plan"); code != 0 || !strings.Contains(stdout, "No changes: project shop matches the file") {
		t.Fatalf("plan of the export: exit %d\n%s%s", code, stdout, stderr)
	}

	edited := filepath.Join(dir, "edited.toml")
	file := `version = 1
project = "shop"
[[environments]]
  name = "production"
  [[environments.services]]
    name = "web"
    image = "web:1"
    port = 8080
  [[environments.services]]
    name = "worker"
    image = "worker:1"
    variables = ["QUEUE_TOKEN"]
`
	if err := os.WriteFile(edited, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := exec(t, "apply", edited, "--dry-run")
	if code != 0 || !strings.Contains(stdout, "+  production/worker  service") || !strings.Contains(stdout, "~  production/web     port") ||
		!strings.Contains(stdout, "Needs you:") || !strings.Contains(stdout, "QUEUE_TOKEN") {
		t.Fatalf("plan: exit %d\n%s%s", code, stdout, stderr)
	}
	plan := ok[apitypes.ApplyPlan](t, "apply", edited, "--plan")
	if len(plan.Changes) != 3 || len(plan.Manual) != 1 {
		t.Fatalf("plan --json = %+v", plan)
	}

	res := ok[apitypes.ApplyResult](t, "apply", edited)
	if len(res.Staged) != 1 || len(res.Staged[0].Services) != 1 || len(res.Applied) != 0 {
		t.Fatalf("staged = %+v", res)
	}
	if code, stdout, _ := exec(t, "changes", "-p", "shop", "-e", "production"); code != 0 || !strings.Contains(stdout, "service (created when applied)") {
		t.Fatalf("changes:\n%s", stdout)
	}
	code, stdout, stderr = exec(t, "apply", edited, "--deploy")
	if code != 0 || !strings.Contains(stdout+stderr, "Applied shop/production: 2 releases started") {
		t.Fatalf("--deploy: exit %d\n%s%s", code, stdout, stderr)
	}
	// A release the apply starts that fails exits deploy_failed.
	srv.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	if err := os.WriteFile(edited, []byte(strings.Replace(file, "8080", "9090", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := exec(t, "apply", edited, "--deploy"); code != int(output.ExitDeployFailed) || !strings.Contains(stderr, "release 3 failed") {
		t.Fatalf("failed --deploy: exit %d\n%s%s", code, stdout, stderr)
	}
	if code, _, stderr := exec(t, "apply", "--plan", "-p", "shop", "-e", "production"); code != 2 || !strings.Contains(stderr, "for a project file") {
		t.Fatalf("--plan without a file: exit %d %s", code, stderr)
	}
}
