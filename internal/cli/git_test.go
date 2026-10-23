package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/core"
)

// `dock git connect` prints the webhook secret once and the GitHub
// settings; the token goes in through stdin and never comes back out.
func TestGitConnect(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	ctx := context.Background()
	if _, err := srv.core.CreateProject(ctx, sys, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.core.CreateService(ctx, sys, core.EnvRef{Project: "shop", Environment: "production"}, apitypes.CreateServiceRequest{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := execStdin(t, srv.tokens[apitypes.ScopeDeploy], "login", srv.url, "--token-stdin"); code != 0 {
		t.Fatalf("login: %s", stderr)
	}
	target := []string{"-p", "shop", "-e", "production", "-s", "web"}
	const token = "github_pat_11ABC"

	code, stdout, stderr := execStdin(t, token+"\n", append([]string{"git", "connect", "acme/shop", "--dir", "apps/web", "--token-stdin", "--status"}, target...)...)
	if code != 0 || !strings.Contains(stdout, "Connected shop/production/web: pushes to acme/shop@main in apps/web/ deploy it") ||
		!strings.Contains(stdout, "Content type") || !strings.Contains(stdout, "Secret") || strings.Contains(stdout+stderr, token) {
		t.Fatalf("connect: %d\n%s\n%s", code, stdout, stderr)
	}
	// No [hooks] public_host on the test server: connect says how to set it.
	if !strings.Contains(stderr, "[hooks] public_host") {
		t.Errorf("no hint about public_host: %s", stderr)
	}

	src := ok[apitypes.GitSource](t, append([]string{"git", "status"}, target...)...)
	if c := src.Connection; c == nil || !c.TokenSet || !c.Status || c.Dir != "apps/web" || src.HooksReady {
		t.Fatalf("status: %+v", src)
	}
	// Updating keeps the secret to itself.
	code, stdout, _ = exec(t, append([]string{"git", "connect", "acme/shop", "--branch", "release"}, target...)...)
	if code != 0 || strings.Contains(stdout, "Secret") || !strings.Contains(stdout, "token     set") {
		t.Fatalf("update: %d\n%s", code, stdout)
	}

	if code, _, _ := exec(t, append([]string{"git", "disconnect"}, target...)...); code != int(output.ExitUsage) {
		t.Fatalf("disconnect without --yes off a terminal: exit %d", code)
	}
	ok[struct{ Disconnected bool }](t, append([]string{"git", "disconnect", "--yes"}, target...)...)
	if src := ok[apitypes.GitSource](t, append([]string{"git", "status"}, target...)...); src.Connection != nil {
		t.Fatalf("still connected: %+v", src)
	}
}
