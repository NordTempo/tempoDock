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
