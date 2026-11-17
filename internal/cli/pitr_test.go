package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/core"
)

// `dock backup pitr` and `dock backup restore --to-time`: flags that
// don't go together are usage errors before anything is sent, an
// in-place restore asks first, and the server's refusal (backups not
// off-box) reaches the user as it says.
func TestPITRCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	ref := core.ServiceRef{Project: "shop", Environment: "production", Service: "db"}
	if _, err := srv.core.CreateService(context.Background(), sys, ref.Env(), apitypes.CreateServiceRequest{Name: "db", Template: apitypes.TemplatePostgres}); err != nil {
		t.Fatal(err)
	}
	at := []string{"-p", "shop", "-e", "production", "-s", "db"}
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"backup", "restore"}, "--snapshot or --to-time"},
		{[]string{"backup", "restore", "--snapshot", "4cf53263", "--to-time", "now"}, "not both"},
		{[]string{"backup", "restore", "--snapshot", "4cf53263", "--in-place"}, "--in-place restores a time"},
		{[]string{"backup", "restore", "--to-time", "yesterday"}, "isn't a time"},
		{[]string{"backup", "restore", "--to-time", "2026-11-17T14:01Z", "--in-place"}, "--yes"},
		{[]string{"backup", "pitr", "maybe"}, "is not on or off"},
	} {
		if code, _, stderr := exec(t, append(c.args, at...)...); code != int(output.ExitUsage) || !strings.Contains(stderr, c.says) {
			t.Errorf("%v: exit %d %q", c.args, code, stderr)
		}
	}
	t.Setenv(clientconfig.EnvToken, srv.platform)
	if code, _, stderr := exec(t, append([]string{"backup", "pitr", "on"}, at...)...); code == 0 || !strings.Contains(stderr, "storage connection") {
		t.Errorf("pitr on with local backups: exit %d %q", code, stderr)
	}
}
