package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// `dock import compose` sends the Compose file with its .env and env_file
// files, prints warnings and the plan without printing a secret, writes
// the project file with -o (never over a file), and with --apply --yes
// creates the project.
func TestImportCompose(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	dir := filepath.Join(t.TempDir(), "My_Stack")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("docker-compose.yml", `services:
  web:
    image: ghcr.io/acme/web:${TAG:-1}
    env_file: web.env
    environment:
      API_TOKEN: ${API_TOKEN}
    privileged: true
`)
	write(".env", "API_TOKEN=tok-from-dotenv\n")
	write("web.env", "MODE=production\n")
	compose := filepath.Join(dir, "docker-compose.yml")
	out := filepath.Join(dir, "dock.project.toml")

	code, stdout, stderr := exec(t, "import", "compose", compose, "-o", out)
	if code != 0 || !strings.Contains(stderr, "Privileged mode") || !strings.Contains(stdout, "+  production/web") ||
		strings.Contains(stdout+stderr, "tok-from-dotenv") || !strings.Contains(stderr, "Secret web.API_TOKEN: value from the Compose file") {
		t.Fatalf("plan: exit %d\n%s%s", code, stdout, stderr)
	}
	file, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(file), "MODE = 'production'") || !strings.Contains(string(file), "ghcr.io/acme/web:1") || strings.Contains(string(file), "tok-from-dotenv") {
		t.Fatalf("project file: %v\n%s", err, file)
	}
	if code, _, stderr := exec(t, "import", "compose", compose, "-o", out); code != 2 || !strings.Contains(stderr, "exists") {
		t.Fatalf("-o over a file: exit %d %s", code, stderr)
	}

	res := ok[apitypes.ComposeImport](t, "import", "compose", compose, "--apply", "--yes")
	if res.Result == nil || len(res.Result.Applied) != 1 || res.Plan.Project != "my-stack" {
		t.Fatalf("apply = %+v", res)
	}
	if code, stdout, _ := exec(t, "service", "list", "-p", "my-stack", "-e", "production"); code != 0 || !strings.Contains(stdout, "web") {
		t.Fatalf("service list:\n%s", stdout)
	}
}

// Updating a project from a Compose file that drops a service: the plan
// keeps it; --prune --stage stages its archive for review (an admin's),
// and a deploy token is refused the archive.
func TestImportComposeUpdate(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	dir := t.TempDir()
	compose := filepath.Join(dir, "compose.yaml")
	write := func(content string) {
		if err := os.WriteFile(compose, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("services:\n  web:\n    image: web:1\n  worker:\n    image: worker:1\n")
	if code, _, stderr := exec(t, "import", "compose", compose, "-p", "shop", "--apply", "--yes"); code != 0 {
		t.Fatalf("create: exit %d %s", code, stderr)
	}
	write("services:\n  web:\n    image: web:2\n")
	if code, stdout, stderr := exec(t, "import", "compose", compose, "-p", "shop"); code != 0 || !strings.Contains(stdout, "worker") || !strings.Contains(stderr, "--apply") {
		t.Fatalf("plan: exit %d\n%s%s", code, stdout, stderr)
	}
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	if code, _, _ := exec(t, "import", "compose", compose, "-p", "shop", "--prune", "--stage", "--yes"); code == 0 {
		t.Fatal("a deploy token staged an archive")
	}
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	res := ok[apitypes.ComposeImport](t, "import", "compose", compose, "-p", "shop", "--prune", "--stage", "--yes")
	if res.Result == nil || len(res.Result.Applied) != 0 || len(res.Result.Staged) != 1 || len(res.Result.Staged[0].Services) != 1 || res.Result.Staged[0].Services[0].Service != "worker" {
		t.Fatalf("stage = %+v", res.Result)
	}
}
