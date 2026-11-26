package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// project delete refuses a live project, needs the name typed again
// (--confirm without a terminal) and frees the name.
func TestProjectDelete(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	ok[apitypes.Project](t, "project", "create", "shop")
	if code, _, stderr := exec(t, "project", "delete", "shop", "--confirm", "shop"); code == 0 || !strings.Contains(stderr, "dock project archive shop") {
		t.Fatalf("live project: %d %s", code, stderr)
	}
	ok[apitypes.Project](t, "project", "archive", "shop", "--yes")
	if code, _, stderr := exec(t, "project", "delete", "shop"); code == 0 || !strings.Contains(stderr, "--confirm") {
		t.Fatalf("no confirmation: %d %s", code, stderr)
	}
	if code, _, stderr := exec(t, "project", "delete", "shop", "--confirm", "shp"); code == 0 || !strings.Contains(stderr, "type the project's name") {
		t.Fatalf("mistyped: %d %s", code, stderr)
	}
	got := ok[apitypes.ProjectDeleted](t, "project", "delete", "shop", "--confirm", "shop")
	if got.Project != "shop" || got.Environments != 1 {
		t.Fatalf("deleted = %+v", got)
	}
	ok[apitypes.Project](t, "project", "create", "shop")
}

// environment delete needs the name typed again (--confirm without a
// terminal) and frees the name.
func TestEnvironmentDelete(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	ok[apitypes.Project](t, "project", "create", "shop")
	ok[apitypes.Environment](t, "environment", "create", "staging", "-p", "shop")
	if code, _, stderr := exec(t, "environment", "delete", "staging", "-p", "shop"); code == 0 || !strings.Contains(stderr, "--confirm") {
		t.Fatalf("no confirmation: %d %s", code, stderr)
	}
	got := ok[apitypes.EnvironmentDeleted](t, "environment", "delete", "staging", "-p", "shop", "--confirm", "staging")
	if got.Project != "shop" || got.Environment != "staging" {
		t.Fatalf("deleted = %+v", got)
	}
	ok[apitypes.Environment](t, "environment", "create", "staging", "-p", "shop")
}
