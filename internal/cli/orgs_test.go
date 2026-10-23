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
	if code, _, stderr := exec(t, "org", "create", "side"); code == 0 || !strings.Contains(stderr, "can't create organizations") {
		t.Fatalf("a token creating an org: %d %s", code, stderr)
	}
}
