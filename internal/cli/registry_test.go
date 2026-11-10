package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// `dock registry login` reads the password from stdin and never prints
// it; `logout` removes it.
func TestRegistryLoginCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	const secret = "ghp_s3cret"

	if code, _, stderr := exec(t, "registry", "login", "ghcr.io", "--username", "bot"); code != int(output.ExitUsage) || !strings.Contains(stderr, "--password-stdin") {
		t.Fatalf("no password and no terminal: %d %s", code, stderr)
	}
	code, stdout, stderr := execStdin(t, secret+"\n", "registry", "login", "GHCR.io", "-u", "bot", "--password-stdin", "--json")
	if code != 0 || !strings.Contains(stdout, `"host": "ghcr.io"`) || strings.Contains(stdout+stderr, secret) {
		t.Fatalf("login: %d\n%s%s", code, stdout, stderr)
	}
	code, stdout, _ = exec(t, "registry", "credentials")
	if code != 0 || !strings.Contains(stdout, "ghcr.io") || !strings.Contains(stdout, "bot") || strings.Contains(stdout, secret) {
		t.Fatalf("credentials: %d\n%s", code, stdout)
	}

	if removed := ok[map[string]string](t, "registry", "logout", "ghcr.io"); removed["removed"] != "ghcr.io" {
		t.Fatalf("logout = %v", removed)
	}
	if list := ok[[]apitypes.RegistryCredential](t, "registry", "credentials"); len(list) != 0 {
		t.Fatalf("credentials after logout = %+v", list)
	}
	if code, _, stderr := exec(t, "registry", "logout", "ghcr.io"); code == 0 || !strings.Contains(stderr, "no credentials for registry ghcr.io") {
		t.Fatalf("logout again: %d %s", code, stderr)
	}
}
