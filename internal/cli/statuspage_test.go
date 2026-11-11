package cli

import (
	"context"
	"strconv"
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

// An incident is drafted, published, updated, fixed and resolved from
// the CLI, and changing its component keeps it named.
func TestIncidentCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	if _, err := srv.core.CreateService(context.Background(), sys, core.EnvRef{Project: "shop", Environment: "production"}, apitypes.CreateServiceRequest{Name: "api", Port: 80}); err != nil {
		t.Fatal(err)
	}
	ok[apitypes.StatusPage](t, "status-page", "create", "acme")
	ok[apitypes.StatusPage](t, "status-page", "component", "add", "acme", "API", "--service", "shop/production/api")

	if code, _, stderr := exec(t, "incident", "open", "acme", "Slow"); code != int(output.ExitUsage) || !strings.Contains(stderr, "--draft") {
		t.Fatalf("open without a message: %d %q", code, stderr)
	}
	if code, _, stderr := exec(t, "incident", "open", "acme", "Slow", "--component", "API", "-m", "x"); code != int(output.ExitUsage) || !strings.Contains(stderr, "API:partial") {
		t.Fatalf("a component without an impact: %d %q", code, stderr)
	}
	inc := ok[apitypes.StatusIncident](t, "incident", "open", "acme", "API errors", "--component", "API:partial", "--draft", "-m", "Looking into it.")
	if inc.State != apitypes.IncidentDraft || inc.Components[0].Impact != apitypes.StatusPartialOutage {
		t.Fatalf("draft = %+v", inc)
	}
	if inc = ok[apitypes.StatusIncident](t, "incident", "publish", inc.ID); inc.State != apitypes.IncidentInvestigating || inc.Updates[0].Message != "Looking into it." {
		t.Fatalf("published = %+v", inc)
	}
	inc = ok[apitypes.StatusIncident](t, "incident", "update", inc.ID, "--state", "identified", "-m", "Found it", "--component", "API:major")
	if inc.State != apitypes.IncidentIdentified || inc.Components[0].Impact != apitypes.StatusMajorOutage {
		t.Fatalf("updated = %+v", inc)
	}
	if inc = ok[apitypes.StatusIncident](t, "incident", "update", inc.ID, "-m", "Still on it."); inc.State != apitypes.IncidentIdentified {
		t.Fatalf("an update keeps the state: %+v", inc)
	}
	inc = ok[apitypes.StatusIncident](t, "incident", "edit", inc.ID, "--update", strconv.FormatInt(inc.Updates[1].ID, 10), "-m", "Found it.")
	if inc.Updates[1].Message != "Found it." || inc.Updates[1].EditedAt == nil {
		t.Fatalf("edited = %+v", inc.Updates)
	}
	ok[apitypes.StatusPage](t, "status-page", "component", "add", "acme", "API", "--service", "shop/production/api", "--group", "Core")
	if code, stdout, _ := exec(t, "incident", "show", inc.ID); code != 0 || !strings.Contains(stdout, "API major outage") || !strings.Contains(stdout, "Found it. (edited)") {
		t.Fatalf("show = %d\n%s", code, stdout)
	}
	if inc = ok[apitypes.StatusIncident](t, "incident", "resolve", inc.ID); inc.State != apitypes.IncidentResolved || inc.ResolvedAt == nil {
		t.Fatalf("resolved = %+v", inc)
	}
	if list := ok[[]apitypes.StatusIncident](t, "incident", "list"); len(list) != 1 || list[0].Page != "acme" {
		t.Fatalf("list = %+v", list)
	}
	ok[map[string]string](t, "incident", "remove", inc.ID, "-y")
	if code, _, _ := exec(t, "incident", "show", inc.ID); code != int(output.ExitNotFound) {
		t.Fatalf("show a removed incident: %d", code)
	}
}
