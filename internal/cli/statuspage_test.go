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

// A status page is made, given components, ordered and published from
// the CLI; components keep their sources when another one changes.
func TestStatusPageCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	for _, name := range []string{"web", "api"} {
		if _, err := srv.core.CreateService(context.Background(), sys, core.EnvRef{Project: "shop", Environment: "production"}, apitypes.CreateServiceRequest{Name: name, Port: 80}); err != nil {
			t.Fatal(err)
		}
	}

	if p := ok[apitypes.StatusPage](t, "status-page", "create", "acme", "--title", "Acme"); p.Slug != "acme" || p.Published {
		t.Fatalf("create = %+v", p)
	}
	ok[apitypes.StatusPage](t, "status-page", "component", "add", "acme", "Web app", "--service", "shop/production/web", "--group", "Core")
	p := ok[apitypes.StatusPage](t, "status-page", "component", "add", "acme", "API", "--service", "shop/production/api", "--group", "Core")
	if len(p.Components) != 2 || p.Components[1].Sources[0].Service != "shop/production/api" {
		t.Fatalf("components = %+v", p.Components)
	}
	p = ok[apitypes.StatusPage](t, "status-page", "component", "move", "acme", "api", "--to", "1")
	if p.Components[0].Name != "API" || p.Components[1].Sources[0].Service != "shop/production/web" {
		t.Fatalf("after move = %+v", p.Components)
	}
	if code, _, stderr := exec(t, "status-page", "component", "add", "acme", "Docs"); code != int(output.ExitUsage) || !strings.Contains(stderr, "--manual") {
		t.Fatalf("a component without a source: %d %q", code, stderr)
	}
	if code, _, stderr := exec(t, "status-page", "component", "rm", "acme", "Nope"); code != int(output.ExitNotFound) {
		t.Fatalf("removing a component that isn't there: %d %q", code, stderr)
	}

	p = ok[apitypes.StatusPage](t, "status-page", "publish", "acme")
	if !p.Published || p.URL != "https://status-acme.apps.test" {
		t.Fatalf("publish = %+v", p.StatusPageSummary)
	}
	if code, stdout, _ := exec(t, "status-page", "show", "acme"); code != 0 || !strings.Contains(stdout, "published at https://status-acme.apps.test") ||
		!strings.Contains(stdout, "shop/production/api") {
		t.Fatalf("show = %d\n%s", code, stdout)
	}
	if pages := ok[[]apitypes.StatusPageSummary](t, "status-page", "list"); len(pages) != 1 || pages[0].Components != 2 {
		t.Fatalf("list = %+v", pages)
	}
	if code, _, _ := exec(t, "status-page", "remove", "acme"); code != int(output.ExitUsage) {
		t.Fatalf("remove without --yes: %d", code)
	}
	ok[map[string]string](t, "status-page", "remove", "acme", "--yes")
	if code, _, stderr := exec(t, "status-page", "show", "acme"); code != int(output.ExitNotFound) || !strings.Contains(stderr, "dock status-page list") {
		t.Fatalf("show a removed page: %d %q", code, stderr)
	}
}
