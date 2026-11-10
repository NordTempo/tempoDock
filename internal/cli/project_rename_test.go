package cli

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// Linked directories follow the original identity even when its old name is reused.
func TestProjectRenameLinkedDirectory(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	appDir(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	before := ok[apitypes.Project](t, "project", "create", "shop")
	ok[apitypes.Service](t, "service", "create", "web", "-p", "shop", "-e", "production", "--image", "app:1")
	ok[linkView](t, "link", "-p", "shop", "-s", "web")
	renamed := ok[apitypes.Project](t, "project", "rename", "shop", "storefront")
	if renamed.ID != before.ID || renamed.Name != "storefront" {
		t.Fatalf("renamed: %+v", renamed)
	}
	ok[apitypes.Project](t, "project", "create", "shop")
	got := ok[apitypes.ServiceOverview](t, "status")
	if got.Service.Project != "storefront" {
		t.Fatalf("linked status followed old name: %+v", got)
	}
	relinked := ok[linkView](t, "link", "-s", "web")
	if relinked.Project.Name != "storefront" || relinked.Project.ID != itoa(before.ID) {
		t.Fatalf("relinked: %+v", relinked)
	}
	code, stdout, stderr := exec(t, "project", "rename", "storefront", "next")
	if code != 0 || !strings.Contains(stdout, "Renamed project storefront to next") {
		t.Fatalf("human output: %d %s", code, stderr)
	}
}
