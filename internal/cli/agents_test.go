package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// `dock agent settings` and `dock agent set` from a `dock login`: the
// agent reads its person's setting and tightens it, but can't loosen it.
func TestAgentCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	ctx := context.Background()
	if _, err := srv.core.CreateUser(ctx, sys, apitypes.CreateUserRequest{
		Email: "ada@example.com", Name: "Ada", Password: "a long enough password", Role: apitypes.ScopeDeploy,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := srv.core.Login(ctx, "ada@example.com", "a long enough password", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := browserLogin(t, srv, func(c string) error { return srv.core.DecideCLILogin(ctx, res.Actor, c, "127.0.0.1", true) }); code != 0 {
		t.Fatal(stderr)
	}
	cur := ok[apitypes.AgentSettings](t, "agent", "settings")
	if len(cur.Settings) < 30 || cur.Settings[0].Level != "all" {
		t.Fatalf("settings = %+v", cur)
	}
	next := ok[apitypes.AgentSettings](t, "agent", "set", "deploy=view", "--kinds", "production", "--ask")
	for _, s := range next.Settings {
		if s.Area == "deploy" && s.Kind == "production" && (s.Level != "view" || !s.Ask) {
			t.Fatalf("deploy in production = %+v", s)
		}
	}
	if code, _, stderr := exec(t, "agent", "set", "deploy=edit", "--kinds", "production"); code == 0 || !strings.Contains(stderr, "Account page") {
		t.Fatalf("an agent loosening: exit %d %s", code, stderr)
	}
}
