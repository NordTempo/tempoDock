package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// A context keeps a platform login beside its org login (ADR 0080): org
// commands use the org's, and a call the server says runs the platform
// goes again with the platform's. whoami shows both; logging out of the
// platform leaves the org login, and platform commands then say how to
// get one back.
func TestPlatformLoginBesideOrgLogin(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	org, err := srv.core.CreateToken(context.Background(), sys, apitypes.CreateTokenRequest{Name: "org-admin", Scope: apitypes.ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{org.Secret, srv.platform} {
		if code, _, stderr := execStdin(t, tok+"\n", "login", srv.url, "--token-stdin"); code != 0 {
			t.Fatal(stderr)
		}
	}
	type whoami struct {
		apitypes.Me
		PlatformCredential *apitypes.Me `json:"platform_credential"`
	}
	if me := ok[whoami](t, "whoami"); me.Name != "org-admin" || me.PlatformCredential == nil || !me.PlatformCredential.PlatformLogin {
		t.Fatalf("whoami = %+v", me)
	}
	ok[apitypes.Project](t, "project", "create", "blog")
	if servers := ok[[]apitypes.Server](t, "server", "list"); len(servers) == 0 {
		t.Fatal("no servers listed")
	}

	ok[map[string]any](t, "logout", "--platform")
	code, _, stderr := exec(t, "server", "list")
	if code != int(output.ExitAuth) || !strings.Contains(stderr, "dock login --platform") {
		t.Fatalf("server list without a platform login: exit %d %s", code, stderr)
	}
	ok[apitypes.Project](t, "project", "create", "docs")
}
