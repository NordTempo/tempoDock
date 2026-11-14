package cli

import (
	"context"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// Groups, one-off grants and effective access from the CLI (ADR 0080):
// a Contractors group with Deploy edit on shop's production, a member in
// it, a one-off grant beside it, and `dock can` showing what adds up.
func TestGroupCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	if _, err := srv.core.CreateUser(context.Background(), sys, apitypes.CreateUserRequest{
		Email: "ola@example.com", Name: "Ola", Password: "a long enough password", Role: apitypes.ScopeRead,
	}); err != nil {
		t.Fatal(err)
	}

	if roles := ok[[]apitypes.AreaRole](t, "role", "list"); len(roles) < 10 {
		t.Fatalf("role list = %+v", roles)
	}
	ok[apitypes.Group](t, "group", "create", "Contractors")
	g := ok[apitypes.Group](t, "group", "grant", "Contractors", "deploy:edit", "-p", "shop", "-e", "production")
	if len(g.Grants) != 1 || g.Grants[0].Environment != "production" {
		t.Fatalf("grant = %+v", g)
	}
	ok[apitypes.Group](t, "group", "remove", "Viewers", "ola@example.com")
	if g := ok[apitypes.Group](t, "group", "add", "Contractors", "ola@example.com"); len(g.Members) != 1 {
		t.Fatalf("add = %+v", g)
	}
	if gr := ok[apitypes.Grant](t, "grant", "ola@example.com", "logs:view", "-p", "shop", "--for", "4h"); gr.ExpiresAt == nil || gr.User != "ola@example.com" {
		t.Fatalf("one-off grant = %+v", gr)
	}
	if list := ok[[]apitypes.Grant](t, "grant", "list"); len(list) != 1 {
		t.Fatalf("grant list = %+v", list)
	}
	acc := ok[apitypes.EffectiveAccess](t, "can", "ola@example.com")
	if len(acc.Environments) != 1 || acc.Environments[0].Environment != "production" || len(acc.Groups) != 1 {
		t.Fatalf("can = %+v", acc)
	}
	var deploy string
	for _, r := range acc.Environments[0].Roles {
		if r.Role == "deploy" {
			deploy = r.Level
		}
	}
	if deploy != "edit" {
		t.Fatalf("deploy level = %q in %+v", deploy, acc.Environments[0].Roles)
	}
}
