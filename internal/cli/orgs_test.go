package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// The org to act in reaches the server from --org and DOCK_ORG, `dock
// link` remembers the org the project is in, and `dock org` lists and
// switches between yours.
func TestOrgSelection(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	appDir(t)
	if code, _, stderr := execStdin(t, srv.tokens[apitypes.ScopeAdmin]+"\n", "login", srv.url, "--token-stdin"); code != 0 {
		t.Fatalf("login: %s", stderr)
	}

	orgs := ok[[]orgView](t, "org", "list")
	if len(orgs) != 1 || orgs[0].Slug != "test" || !orgs[0].Current {
		t.Fatalf("org list = %+v", orgs)
	}
	ok[apitypes.Project](t, "project", "create", "shop")
	if link := ok[linkView](t, "link", "-p", "shop"); link.Org != "test" {
		t.Fatalf("link = %+v, want org test", link)
	}

	// The token belongs to test, so naming another org is refused.
	if code, _, stderr := exec(t, "project", "list", "--org", "acme"); code == 0 || !strings.Contains(stderr, "belongs to organization test") {
		t.Fatalf("--org acme: %d %s", code, stderr)
	}
	t.Setenv(clientconfig.EnvOrg, "acme")
	if code, _, stderr := exec(t, "project", "list"); code == 0 || !strings.Contains(stderr, "belongs to organization test") {
		t.Fatalf("DOCK_ORG=acme: %d %s", code, stderr)
	}
	t.Setenv(clientconfig.EnvOrg, "")

	if code, _, stderr := exec(t, "org", "use", "acme"); code == 0 || !strings.Contains(stderr, "not found among yours") {
		t.Fatalf("org use acme: %d %s", code, stderr)
	}
	if v := ok[orgView](t, "org", "use", "test"); v.Slug != "test" {
		t.Fatalf("org use test = %+v", v)
	}
}

// The organization's settings from the CLI: variables, invites and its
// name.
func TestOrgSettingsCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	appDir(t)
	if code, _, stderr := execStdin(t, srv.tokens[apitypes.ScopeAdmin]+"\n", "login", srv.url, "--token-stdin"); code != 0 {
		t.Fatalf("login: %s", stderr)
	}

	set := ok[[]apitypes.OrgVariable](t, "org", "env", "set", "SENTRY_DSN=https://x")
	if len(set) != 1 || set[0].Key != "SENTRY_DSN" {
		t.Fatalf("env set = %+v", set)
	}
	vars := ok[[]apitypes.OrgVariable](t, "org", "env", "list", "--reveal")
	if len(vars) != 1 || vars[0].Value == nil || *vars[0].Value != "https://x" {
		t.Fatalf("env list = %+v", vars)
	}

	inv := ok[inviteView](t, "org", "invite", "bob@example.com", "--role", "read")
	if !strings.HasPrefix(inv.URL, srv.url+"/invite/dinv_") || inv.Invite.Role != apitypes.ScopeRead {
		t.Fatalf("invite = %+v", inv)
	}
	if list := ok[[]apitypes.Invite](t, "org", "invites"); len(list) != 1 {
		t.Fatalf("invites = %+v", list)
	}
	ok[map[string]any](t, "org", "invites", "revoke", itoa(inv.Invite.ID))
	if list := ok[[]apitypes.Invite](t, "org", "invites"); len(list) != 0 {
		t.Fatalf("invites after revoke = %+v", list)
	}

	if o := ok[apitypes.Org](t, "org", "rename", "Test Org"); o.Name != "Test Org" || o.Slug != "test" {
		t.Fatalf("rename = %+v", o)
	}

	// A new slug: this machine's context and link follow it, and the old
	// one keeps working (ADR 0064).
	ok[orgView](t, "org", "use", "test")
	ok[apitypes.Project](t, "project", "create", "shop")
	ok[linkView](t, "link", "-p", "shop")
	if o := ok[apitypes.Org](t, "org", "rename", "--slug", "nordtempo"); o.Slug != "nordtempo" || o.Name != "Test Org" {
		t.Fatalf("rename --slug = %+v", o)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range cfg.Contexts {
		if c.Org != "nordtempo" {
			t.Errorf("context %s acts in %q, want nordtempo", name, c.Org)
		}
	}
	for dir, l := range cfg.Links {
		if l.Org != "nordtempo" {
			t.Errorf("link %s is in %q, want nordtempo", dir, l.Org)
		}
	}
	if p := ok[[]apitypes.Project](t, "project", "list", "--org", "test"); len(p) != 1 {
		t.Fatalf("project list --org test = %+v", p)
	}
	if v := ok[orgView](t, "org", "use", "test"); v.Slug != "nordtempo" {
		t.Fatalf("org use with the old slug = %+v", v)
	}
}
