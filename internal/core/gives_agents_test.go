package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Nobody gives more than they hold (ADR 0080), and an agent holds what it
// asks first for only after asking: it can't hand that on to a token, a
// person, a group or a role, which wouldn't ask. Every giving operation
// refuses it, for a person's agent that asks them first and for a token
// the organization's agent rules make ask; a person gives it. Where the
// agent doesn't ask, it gives as before.
func TestAnAskingAgentGivesNothingItAsksFor(t *testing.T) {
	prod := apitypes.GrantRequest{Role: "deploy:edit", Project: "shop", Environment: "production"}
	gives := []struct {
		name string
		give func(h *harness, a Actor, vi Actor) error
	}{
		{"a token", func(h *harness, a, _ Actor) error {
			_, err := h.c.CreateToken(h.ctx, a, apitypes.CreateTokenRequest{Name: "helper", Scope: apitypes.ScopeDeploy, Project: "shop", Environment: "production"})
			return err
		}},
		{"a grant to a token", func(h *harness, a, _ Actor) error {
			_, err := h.c.GrantAgent(h.ctx, a, "ci", prod)
			return err
		}},
		{"a group to a token", func(h *harness, a, _ Actor) error {
			_, err := h.c.SetGroupAgent(h.ctx, a, "Deployers", "ci", true)
			return err
		}},
		{"a grant to a member", func(h *harness, a, vi Actor) error {
			_, err := h.c.GrantMember(h.ctx, a, vi.Email, prod)
			return err
		}},
		{"a grant to a group", func(h *harness, a, _ Actor) error {
			_, err := h.c.GrantGroup(h.ctx, a, "Empty", prod)
			return err
		}},
		{"a group to a member", func(h *harness, a, vi Actor) error {
			_, err := h.c.AddGroupMember(h.ctx, a, "Deployers", vi.Email)
			return err
		}},
		{"an invite's role", func(h *harness, a, _ Actor) error {
			_, err := h.c.CreateInvite(h.ctx, a, apitypes.CreateInviteRequest{Email: "new@example.com", Role: apitypes.ScopeDeploy})
			return err
		}},
		{"a member's role", func(h *harness, a, vi Actor) error {
			_, err := h.c.SetMemberRole(h.ctx, a, vi.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeDeploy})
			return err
		}},
		{"a role's parts", func(h *harness, a, _ Actor) error {
			_, err := h.c.CreateRole(h.ctx, a, apitypes.RoleRequest{Name: "Releaser", Includes: []string{"deploy:edit"}})
			return err
		}},
	}
	asking := apitypes.AgentSettings{Settings: []apitypes.AgentSetting{{Area: "deploy", Kind: "production", Level: "all", Ask: true}}}
	agents := []struct {
		name string
		// agent sets up an agent that asks first before deploying to
		// production, and returns its secret.
		agent func(h *harness, admin Actor) string
	}{
		{"a person's agent that asks them first", func(h *harness, _ Actor) string {
			ada := h.cliUser("ada@example.com", apitypes.ScopeAdmin)
			if _, err := h.c.SetAgentSettings(h.ctx, ada, asking); err != nil {
				h.t.Fatal(err)
			}
			return h.cliToken(ada)
		}},
		{"a token the agent rules make ask", func(h *harness, admin Actor) string {
			if _, err := h.c.SetAgentRules(h.ctx, admin, asking); err != nil {
				h.t.Fatal(err)
			}
			boss, err := h.c.createToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "boss", Scope: apitypes.ScopeAdmin})
			if err != nil {
				h.t.Fatal(err)
			}
			return boss.Secret
		}},
	}
	for _, ag := range agents {
		for _, g := range gives {
			t.Run(ag.name+" gives "+g.name, func(t *testing.T) {
				h := newHarness(t)
				h.enforce()
				h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
				if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
					t.Fatal(err)
				}
				admin := h.user("root@example.com", apitypes.ScopeAdmin)
				vi := h.user("vi@example.com", apitypes.ScopeRead)
				if _, err := h.c.createToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeRead}); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"Deployers", "Empty"} {
					if _, err := h.c.CreateGroup(h.ctx, admin, apitypes.CreateGroupRequest{Name: name}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := h.c.GrantGroup(h.ctx, admin, "Deployers", prod); err != nil {
					t.Fatal(err)
				}
				bot, err := h.c.AuthenticateToken(h.ctx, ag.agent(h, admin), "")
				if err != nil {
					t.Fatal(err)
				}
				if r := refusalOf(t, g.give(h, bot, vi)); r.DecidedBy != apitypes.RefusedPerson {
					t.Fatalf("refused by %s: %s", r.DecidedBy, r.Reason)
				}
				// A person gives it.
				if err := g.give(h, admin, vi); err != nil {
					t.Fatalf("a person giving it: %v", err)
				}
			})
		}
		t.Run(ag.name+" gives where it doesn't ask", func(t *testing.T) {
			h := newHarness(t)
			h.enforce()
			h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
			if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
				t.Fatal(err)
			}
			admin := h.user("root@example.com", apitypes.ScopeAdmin)
			bot, err := h.c.AuthenticateToken(h.ctx, ag.agent(h, admin), "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.c.CreateToken(h.ctx, bot, apitypes.CreateTokenRequest{Name: "helper", Scope: apitypes.ScopeDeploy, Project: "shop", Environment: "staging"}); err != nil {
				t.Fatalf("a token for staging: %v", err)
			}
		})
	}
}

// The organization's agent rules cap every agent, so no token lifts them
// off a token, itself or another (one it made, say): a person exempts a
// token. Ending an exemption only tightens, and a token may.
func TestOnlyAPersonExemptsATokenFromAgentRules(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	boss := h.token(testSystem, apitypes.CreateTokenRequest{Name: "boss", Scope: apitypes.ScopeAdmin})
	h.token(testSystem, apitypes.CreateTokenRequest{Name: "helper", Scope: apitypes.ScopeDeploy})
	for _, name := range []string{"boss", "helper"} {
		_, err := h.c.SetAgentRulesBypass(h.ctx, boss, name, true)
		if r := refusalOf(t, err); r.DecidedBy != apitypes.RefusedPerson {
			t.Fatalf("a token exempting %s: %+v", name, r)
		}
	}
	if a, err := h.c.SetAgentRulesBypass(h.ctx, admin, "helper", true); err != nil || !a.RulesBypass {
		t.Fatalf("a person exempting a token: %+v, %v", a, err)
	}
	if a, err := h.c.SetAgentRulesBypass(h.ctx, boss, "helper", false); err != nil || a.RulesBypass {
		t.Fatalf("a token ending an exemption: %+v, %v", a, err)
	}
}

// Asking first is for changes inside an environment, which checkEnv
// decides. An area with none (the org's, the Data panel, approving)
// refuses it rather than keep a setting nothing reads, and one stored
// before reads as not asking.
func TestAskingFirstIsForEnvironmentChanges(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	ada := h.cliUser("ada@example.com", apitypes.ScopeAdmin)
	for _, area := range []string{"members", "org_settings", "data", "status_pages", "alerts", "approver", "usage", "activity"} {
		set := apitypes.AgentSettings{Settings: []apitypes.AgentSetting{{Area: area, Kind: "production", Level: "all", Ask: true}}}
		if _, err := h.c.SetAgentSettings(h.ctx, ada, set); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("a person's agents asking first for %s: %v", area, err)
		}
		if _, err := h.c.SetAgentRules(h.ctx, ada, set); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("agent rules asking first for %s: %v", area, err)
		}
	}
	for area := range askAreas {
		set := apitypes.AgentSettings{Settings: []apitypes.AgentSetting{{Area: area, Kind: "production", Level: "all", Ask: true}}}
		if _, err := h.c.SetAgentSettings(h.ctx, ada, set); err != nil {
			t.Errorf("asking first for %s: %v", area, err)
		}
		// Every area that asks holds a change checkEnv decides.
		var changes Perms
		for _, r := range agentAreas() {
			if r.key == area {
				changes = levelPerms(r, agentAll) & envPerms() &^ readPerms()
			}
		}
		if changes == 0 {
			t.Errorf("%s asks first but changes nothing inside an environment", area)
		}
	}
	// A setting from before the rule reads as not asking.
	if err := h.store.AddAgentSetting(h.ctx, store.AddAgentSettingParams{UserID: ada.UserID, OrgID: 1, Area: "members", Kind: int64(kindProduction), Level: agentAll, Ask: true}); err != nil {
		t.Fatal(err)
	}
	got, err := h.c.AgentSettings(h.ctx, ada, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got.Settings {
		if s.Area == "members" && s.Ask {
			t.Fatalf("members reads as asking: %+v", s)
		}
	}
	bot, err := h.c.AuthenticateToken(h.ctx, h.cliToken(ada), "")
	if err != nil {
		t.Fatal(err)
	}
	if bot.agent.ask[kindIndex(kindProduction)].has(PermMembersManage) {
		t.Fatal("an agent asks first for members.manage, which nothing asks for")
	}
}
