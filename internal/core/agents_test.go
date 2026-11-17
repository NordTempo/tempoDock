package core

import (
	"errors"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A personal agent (ADR 0080): a `dock login` uses its person's access as
// their agent setting lets it. Deploy off in production stops it there and
// not in staging; asking first turns a change into a request only the
// person decides; the agent tightens its own setting but can't loosen it.
func TestPersonalAgents(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.deploy(web, "app:1")
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, web.Project, apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	stagingWeb := h.service2("web", "staging")
	h.deploy(stagingWeb, "app:1")
	ada := h.cliUser("ada@example.com", apitypes.ScopeAdmin)
	agent := func() Actor {
		t.Helper()
		a, err := h.c.AuthenticateToken(h.ctx, h.cliToken(ada), "")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	bot := agent()
	if _, err := h.c.Restart(h.ctx, bot, web); err != nil {
		t.Fatalf("an agent with no setting restarts production: %v", err)
	}

	// The data policy gives Ada everything; with Data panel read outside
	// production, her agents read there.
	if _, err := h.c.SetAgentSettings(h.ctx, ada, apitypes.AgentSettings{Settings: []apitypes.AgentSetting{{Area: "data", Kind: "other", Level: "read"}}}); err != nil {
		t.Fatal(err)
	}
	if l, err := h.c.DataLevel(h.ctx, agent(), EnvRef{"shop", "staging"}); err != nil || l != apitypes.LevelRead {
		t.Fatalf("an agent's data level in staging = %s, %v", l, err)
	}

	// Ada (signed in) turns Deploy off in production for her agents.
	if _, err := h.c.SetAgentSettings(h.ctx, ada, apitypes.AgentSettings{Settings: []apitypes.AgentSetting{
		{Area: "deploy", Kind: "production", Level: "off"},
		{Area: "deploy", Kind: "other", Level: "edit", Ask: true},
	}}); err != nil {
		t.Fatal(err)
	}
	bot = agent()
	_, err := h.c.Restart(h.ctx, bot, web)
	if r := refusalOf(t, err); r.DecidedBy != apitypes.RefusedGrants {
		t.Fatalf("Deploy off in production: %+v", r)
	}

	// In staging it asks Ada first: a request only she decides.
	_, err = h.c.Restart(h.ctx, bot, stagingWeb)
	le, ok := errors.AsType[*LockedError](err)
	if !ok || le.askOwner == nil || *le.askOwner != ada.UserID {
		t.Fatalf("ask first: %v", err)
	}
	if err := h.c.FileRequest(h.ctx, bot, err, Call{Via: "test", Op: "restart", Title: "restart web", Args: []byte(`{"service":"web"}`)}); CodeOf(err) != apitypes.CodeEnvironmentLocked {
		t.Fatalf("filing: %v", err)
	}
	asked, err := h.c.ListRequests(h.ctx, ada, RequestFilter{})
	if err != nil || len(asked) != 1 {
		t.Fatalf("Ada's requests = %+v, %v", asked, err)
	}
	other := h.user("bo@example.com", apitypes.ScopeAdmin)
	if _, err := h.c.DecideRequest(h.ctx, other, asked[0].ID, true, apitypes.DecideAccessRequest{}); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("another admin decided Ada's ask: %v", err)
	}
	if _, err := h.c.DecideRequest(h.ctx, ada, asked[0].ID, false, apitypes.DecideAccessRequest{}); err != nil {
		t.Fatalf("Ada rejects: %v", err)
	}

	// The agent may tighten, not loosen.
	if _, err := h.c.SetAgentSettings(h.ctx, bot, apitypes.AgentSettings{Settings: []apitypes.AgentSetting{{Area: "logs", Kind: "production", Level: "off"}}}); err != nil {
		t.Fatalf("an agent tightening: %v", err)
	}
	if _, err := h.c.SetAgentSettings(h.ctx, bot, apitypes.AgentSettings{Settings: []apitypes.AgentSetting{{Area: "deploy", Kind: "production", Level: "edit"}}}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an agent loosening: %v", err)
	}
	if _, err := h.c.SetAgentSettings(h.ctx, ada, apitypes.AgentSettings{Settings: []apitypes.AgentSetting{{Area: "deploy", Kind: "production", Level: "high"}}}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a level deploy hasn't: %v", err)
	}
}

// A personal agent's data access is its person's (ADR 0080, amended
// 2026-11-17): the data policy judges a `dock login` by its person's
// role, not by what the agent's own grants amount to, which is never
// admin since agents don't approve. So an admin's login gets what tunnels
// need, a deploy member's gets the role:deploy rule, the Data panel
// setting lowers either, and approving changesets stays the Approver
// setting's.
func TestPersonalAgentDataFollowsItsPerson(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, web.Project, apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	perm := func(person Actor, env string) apitypes.DataPermission {
		t.Helper()
		a, err := h.c.AuthenticateToken(h.ctx, h.cliToken(person), "")
		if err != nil {
			t.Fatal(err)
		}
		es, err := h.c.envFor(h.ctx, a, EnvRef{"shop", env}, apitypes.ScopeRead, "reading data access")
		if err != nil {
			t.Fatal(err)
		}
		p, err := h.c.dataPermission(h.ctx, a, es)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	ada := h.cliUser("ada@example.com", apitypes.ScopeAdmin)
	for _, env := range []string{"staging", "production"} {
		if p := perm(ada, env); p.Level != apitypes.LevelWrite || p.Sensitive != apitypes.SensitiveVisible || !p.Manage || p.Approve {
			t.Fatalf("an admin's agent in %s = %+v, want write, visible, managing, not approving", env, p)
		}
	}
	bo := h.cliUser("bo@example.com", apitypes.ScopeDeploy)
	if p := perm(bo, "staging"); p.Level != apitypes.LevelRead || p.Sensitive != apitypes.SensitiveMasked {
		t.Fatalf("a deploy member's agent in staging = %+v, want the role:deploy rule", p)
	}
	if p := perm(bo, "production"); p.Level != apitypes.LevelNone {
		t.Fatalf("a deploy member's agent in production = %+v, want none", p)
	}

	// Ada's setting decides what her agents use of it.
	if _, err := h.c.SetAgentSettings(h.ctx, ada, apitypes.AgentSettings{Settings: []apitypes.AgentSetting{
		{Area: "data", Kind: "production", Level: "propose"},
		{Area: "approver", Kind: "other", Level: agentAll},
	}}); err != nil {
		t.Fatal(err)
	}
	if p := perm(ada, "production"); p.Level != apitypes.LevelPropose || p.Sensitive != apitypes.SensitiveMasked || p.Manage {
		t.Fatalf("Data panel propose in production = %+v", p)
	}
	if p := perm(ada, "staging"); p.Level != apitypes.LevelWrite || !p.Approve {
		t.Fatalf("Approver on outside production = %+v, want approving", p)
	}
}
