package core

import (
	"context"
	"fmt"
	"slices"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Agent settings (ADR 0080, phase 6): a person decides what their agents
// may use of their access in an org, for all of them (agent 0) or one,
// narrowed. An agent may tighten them but never loosen: it acts with the
// person's own `dock login`, so only the person signed in loosens them.

// agentKindNames name agentKinds in the API.
var agentKindNames = [3]string{"production", "other", "previews"}

// agentMatrix is a setting for every area role and kind, with the
// defaults filled in: the person's (agent 0), or an agent's narrowing.
func agentMatrix(rows []store.AgentSetting, agent int64) []apitypes.AgentSetting {
	out := []apitypes.AgentSetting{}
	for _, r := range agentAreas() {
		for i, k := range agentKinds {
			s := apitypes.AgentSetting{Area: r.key, Kind: agentKindNames[i], Level: agentDefault(r.key)}
			if agent != 0 {
				s.Level = agentAll
			}
			for _, row := range rows {
				if row.Area == r.key && uint8(row.Kind) == k && derefOr(row.TokenID) == agent {
					s.Level, s.Ask = row.Level, row.Ask
				}
			}
			out = append(out, s)
		}
	}
	return out
}

// agentFor checks agent is one of the person's own agents: a `dock
// login` of theirs. 0 is all of them.
func (c *Core) agentFor(ctx context.Context, a Actor, agent int64) error {
	if agent == 0 {
		return nil
	}
	t, err := c.store.GetToken(ctx, agent)
	if err != nil || t.UserID == nil || *t.UserID != a.UserID || t.Origin != "cli_login" || t.RevokedAt != nil {
		return notFound("agent %d isn't one of yours; `dock token list --mine` lists your `dock login` tokens", agent)
	}
	return nil
}

// AgentSettings is what the caller's agents may use in this org: all of
// them (agent 0), or one agent's narrowing of that.
func (c *Core) AgentSettings(ctx context.Context, a Actor, agent int64) (apitypes.AgentSettings, error) {
	if a.UserID == 0 || a.OrgID == 0 {
		return apitypes.AgentSettings{}, invalid("agent settings are a person's, in an organization; sign in or `dock login` as yourself")
	}
	if err := c.agentFor(ctx, a, agent); err != nil {
		return apitypes.AgentSettings{}, err
	}
	rows, err := c.store.ListAgentSettings(ctx, store.ListAgentSettingsParams{UserID: a.UserID, OrgID: a.OrgID, TokenID: &agent})
	if err != nil {
		return apitypes.AgentSettings{}, err
	}
	return apitypes.AgentSettings{Agent: agent, Settings: agentMatrix(rows, agent)}, nil
}

// SetAgentSettings replaces what the caller's agents may use (agent 0)
// or one agent's narrowing; cells left out keep their default. Loosening
// (a higher level, or no more asking) needs the person signed in, and
// recently: an agent may only tighten.
func (c *Core) SetAgentSettings(ctx context.Context, a Actor, req apitypes.AgentSettings) (apitypes.AgentSettings, error) {
	cur, err := c.AgentSettings(ctx, a, req.Agent)
	if err != nil {
		return cur, err
	}
	areas := agentAreas()
	next := slices.Clone(cur.Settings)
	for _, s := range req.Settings {
		ai := slices.IndexFunc(areas, func(r areaRole) bool { return r.key == s.Area })
		ki := slices.Index(agentKindNames[:], s.Kind)
		if ai < 0 || ki < 0 {
			return cur, invalid("%s in %s isn't a setting: areas are the built-in roles' (`dock role list`), kinds production, other and previews", s.Area, s.Kind)
		}
		if s.Level != agentAll && s.Level != agentOff && levelRank(areas[ai], s.Level) <= 0 {
			return cur, invalid("%s has no level %q; pick off, all or one of its own (`dock role list`)", s.Area, s.Level)
		}
		next[ai*3+ki] = s
	}
	loosens := false
	for i, s := range next {
		r := areas[i/3]
		was := cur.Settings[i]
		if levelRank(r, s.Level) > levelRank(r, was.Level) || was.Ask && !s.Ask {
			loosens = true
		}
	}
	if loosens {
		if a.Kind == "token" {
			return cur, errf(apitypes.CodeForbidden, "an agent can tighten what agents may do, not loosen it: change it on your Account page in the browser")
		}
		if err := a.requireRecent("loosening what your agents may do"); err != nil {
			return cur, err
		}
	}
	token := &req.Agent
	if req.Agent == 0 {
		token = nil
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteAgentSettings(ctx, store.DeleteAgentSettingsParams{UserID: a.UserID, OrgID: a.OrgID, TokenID: &req.Agent}); err != nil {
			return err
		}
		for i, s := range next {
			def := agentDefault(s.Area)
			if req.Agent != 0 {
				def = agentAll
			}
			if s.Level == def && !s.Ask {
				continue
			}
			if err := q.AddAgentSetting(ctx, store.AddAgentSettingParams{
				UserID: a.UserID, OrgID: a.OrgID, TokenID: token, Area: s.Area, Kind: int64(agentKinds[i%3]), Level: s.Level, Ask: s.Ask,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return cur, err
	}
	which := "all their agents"
	if req.Agent != 0 {
		which = fmt.Sprintf("agent %d", req.Agent)
	}
	c.audit(ctx, a, "agents.settings", nil, nil, a.who(), "changed what "+which+" may do")
	return c.AgentSettings(ctx, a, req.Agent)
}

// AgentRules is the organization's agent rules: for every area role and
// environment kind the most its agents may use (all for no rule) and
// whether their changes there ask an approver first.
func (c *Core) AgentRules(ctx context.Context, a Actor) (apitypes.AgentSettings, error) {
	if err := a.require(apitypes.ScopeAdmin, "reading agent rules"); err != nil {
		return apitypes.AgentSettings{}, err
	}
	rows, err := c.store.ListAgentRules(ctx, a.OrgID)
	if err != nil {
		return apitypes.AgentSettings{}, err
	}
	out := apitypes.AgentSettings{Settings: []apitypes.AgentSetting{}}
	for _, r := range agentAreas() {
		for i, k := range agentKinds {
			s := apitypes.AgentSetting{Area: r.key, Kind: agentKindNames[i], Level: agentAll}
			for _, row := range rows {
				if row.Area == r.key && uint8(row.Kind) == k {
					s.Level, s.Ask = row.Level, row.Ask
				}
			}
			out.Settings = append(out.Settings, s)
		}
	}
	return out, nil
}

// SetAgentRules changes the organization's agent rules: the cells given
// replace theirs. They apply to every agent's next request, personal and
// the org's, but org agents that bypass them.
func (c *Core) SetAgentRules(ctx context.Context, a Actor, req apitypes.AgentSettings) (apitypes.AgentSettings, error) {
	if err := a.require(apitypes.ScopeAdmin, "managing agent rules"); err != nil {
		return apitypes.AgentSettings{}, err
	}
	if err := a.requireRecent("managing agent rules"); err != nil {
		return apitypes.AgentSettings{}, err
	}
	cur, err := c.AgentRules(ctx, a)
	if err != nil {
		return cur, err
	}
	areas := agentAreas()
	next := slices.Clone(cur.Settings)
	for _, s := range req.Settings {
		ai := slices.IndexFunc(areas, func(r areaRole) bool { return r.key == s.Area })
		ki := slices.Index(agentKindNames[:], s.Kind)
		if ai < 0 || ki < 0 {
			return cur, invalid("%s in %s isn't a rule: areas are the built-in roles' (`dock role list`), kinds production, other and previews", s.Area, s.Kind)
		}
		if s.Level != agentAll && s.Level != agentOff && levelRank(areas[ai], s.Level) <= 0 {
			return cur, invalid("%s has no level %q; pick off, all or one of its own (`dock role list`)", s.Area, s.Level)
		}
		next[ai*3+ki] = s
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteAgentRules(ctx, a.OrgID); err != nil {
			return err
		}
		for i, s := range next {
			if s.Level == agentAll && !s.Ask {
				continue
			}
			if err := q.AddAgentRule(ctx, store.AddAgentRuleParams{OrgID: a.OrgID, Area: s.Area, Kind: int64(agentKinds[i%3]), Level: s.Level, Ask: s.Ask}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return cur, err
	}
	c.audit(ctx, a, "agents.rules", nil, nil, "agent rules", "changed the organization's agent rules")
	return c.AgentRules(ctx, a)
}

// SetAgentRulesBypass exempts one of the org's tokens from its agent
// rules, or ends that.
func (c *Core) SetAgentRulesBypass(ctx context.Context, a Actor, ref string, bypass bool) (apitypes.AgentAccess, error) {
	t, err := c.agentToken(ctx, a, ref)
	if err != nil {
		return apitypes.AgentAccess{}, err
	}
	if err := a.require(apitypes.ScopeAdmin, "managing agent rules"); err != nil {
		return apitypes.AgentAccess{}, err
	}
	if err := a.requireRecent("managing agent rules"); err != nil {
		return apitypes.AgentAccess{}, err
	}
	if err := c.store.SetTokenRulesBypass(ctx, store.SetTokenRulesBypassParams{RulesBypass: bypass, ID: t.ID}); err != nil {
		return apitypes.AgentAccess{}, err
	}
	word := "no longer bypasses"
	if bypass {
		word = "bypasses"
	}
	c.audit(ctx, a, "agents.bypass", nil, nil, "token "+t.Name, fmt.Sprintf("token %s %s the agent rules", t.Name, word))
	return c.AgentAccess(ctx, a, t.Name)
}
