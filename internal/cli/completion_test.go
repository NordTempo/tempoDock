package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// TestCompletion: shell completion lists names from the API, narrowed by
// the flags typed, and completes nothing (quietly) without a server.
func TestCompletion(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	appDir(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	exec(t, "service", "create", "worker", "-p", "shop", "-e", "production")
	ok[apitypes.Release](t, "deploy", "-p", "shop", "-e", "production", "-s", "web")

	complete := func(args ...string) []string {
		t.Helper()
		code, stdout, stderr := exec(t, append([]string{cobra.ShellCompRequestCmd}, args...)...)
		// stderr only has cobra's note of the directive, no error.
		if code != 0 || strings.Contains(stderr, "rror") {
			t.Fatalf("complete %v: exit %d\n%s", args, code, stderr)
		}
		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		return lines[:len(lines)-1] // the last line is the directive
	}
	for _, tt := range []struct {
		args []string
		want []string
	}{
		{[]string{"logs", "-p", "shop", "-e", "production", "-s", ""}, []string{"web", "worker"}},
		{[]string{"service", "show", "-p", "shop", "-e", "production", "wo"}, []string{"worker"}},
		{[]string{"deploy", "-p", "shop", "-e", ""}, []string{"production"}},
		{[]string{"project", "archive", ""}, []string{"shop"}},
		{[]string{"release", "show", "-p", "shop", "-e", "production", "-s", "web", ""}, []string{"1\tactive upload"}},
		{[]string{"release", "show", "1", "-p", "shop", "-e", "production", "-s", "web", ""}, nil},
	} {
		if got := complete(tt.args...); !slices.Equal(got, tt.want) {
			t.Errorf("complete %v = %q, want %q", tt.args, got, tt.want)
		}
	}

	t.Setenv(clientconfig.EnvURL, "")
	t.Setenv(clientconfig.EnvToken, "")
	if got := complete("logs", "-p", "shop", "-e", "production", "-s", ""); len(got) != 0 {
		t.Errorf("complete without a server = %q", got)
	}
}
