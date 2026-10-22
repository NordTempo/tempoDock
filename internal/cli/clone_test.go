package cli

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// A deploy token clones an environment (with --deploy, following the
// promoted releases) and is told which values it couldn't copy; the
// export is TOML that reads back as the API's export, with no values.
func TestCloneAndExport(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeDeploy])
	exec(t, "project", "create", "shop")
	for _, args := range [][]string{
		{"deploy", "-p", "shop", "-e", "production", "-s", "web", "--image", "web:1"},
		{"env", "set", "API_KEY=k-7f3a", "-p", "shop", "-e", "production", "-s", "web", "--deploy"},
		{"domain", "add", "shop.example.com", "-p", "shop", "-e", "production", "-s", "web"},
	} {
		if code, _, stderr := exec(t, args...); code != 0 {
			t.Fatalf("%v: %s", args, stderr)
		}
	}

	code, stdout, stderr := exec(t, "env", "clone", "production", "staging", "-p", "shop", "--deploy")
	if code != 0 || !strings.Contains(stdout, "Cloned production into non-production environment shop/staging") ||
		!strings.Contains(stderr, "needs admin: web.API_KEY") || !strings.Contains(stderr, "Not copied: 1 domain, releases, backups.") ||
		!strings.Contains(stdout, "web: release 1 (from production release ") {
		t.Fatalf("clone: exit %d\n%s%s", code, stdout, stderr)
	}

	res := ok[apitypes.CloneResult](t, "environment", "clone", "production", "preview", "-p", "shop", "--kind", "non-production")
	if res.Deploy != nil || len(res.Skipped) != 1 || res.Environment.Name != "preview" {
		t.Fatalf("clone --json = %+v", res)
	}

	code, stdout, stderr = exec(t, "project", "export", "shop")
	if code != 0 || strings.Contains(stdout, "k-7f3a") || !strings.HasPrefix(stdout, "# Tempo Dock project shop") {
		t.Fatalf("export: exit %d\n%s%s", code, stdout, stderr)
	}
	var fromTOML apitypes.ProjectExport
	if err := toml.Unmarshal([]byte(stdout), &fromTOML); err != nil {
		t.Fatalf("export isn't TOML: %v\n%s", err, stdout)
	}
	fromJSON := ok[apitypes.ProjectExport](t, "project", "export", "-p", "shop", "--format", "json")
	if len(fromTOML.Environments) != 3 || fromTOML.Environments[1].Services[0].Domains[0].Hostname != "shop.example.com" ||
		fromTOML.Environments[0].Services[0].Variables != nil || fromJSON.Environments[1].Services[0].Variables[0] != "API_KEY" {
		t.Fatalf("export:\n%s", stdout)
	}
	if code, _, stderr := exec(t, "project", "export", "shop", "--format", "yaml"); code != 2 || !strings.Contains(stderr, "toml or json") {
		t.Fatalf("--format yaml: exit %d %s", code, stderr)
	}
}
