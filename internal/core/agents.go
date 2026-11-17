package core

import (
	"slices"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Personal agents (ADR 0080, phase 6): a person's `dock login` tokens act
// for them, with their access as it is now, capped by their agent
// setting (per area role and environment kind: a level, or off, and
// whether to ask them first) and by the agent's own narrowing, which can
// only take more away. Without a setting an agent uses everything its
// person has but approving, as `dock login` always did.

// Agent levels besides an area role's own: everything it has, or none.
const (
	agentAll = "all"
	agentOff = "off"
)

// agentKinds are the environment kinds in agent settings' order.
var agentKinds = [3]uint8{kindProduction, kindOther, kindPreviews}

// kindIndex is a kind bit's place in agentKinds.
func kindIndex(k uint8) int {
	return slices.Index(agentKinds[:], k)
}

// agentAreas are the area roles agent settings cover: all but Full
// access, which is no area.
func agentAreas() []areaRole {
	return slices.DeleteFunc(slices.Clone(areaRoles), func(r areaRole) bool { return r.key == "full" })
}

// levelPerms is what an area role holds at a level: the levels up to it,
// all of them for agentAll, none for agentOff or a level it hasn't.
func levelPerms(r areaRole, level string) Perms {
	var s Perms
	for _, l := range r.levels {
		s |= l.adds
		if level != agentAll && l.name == level {
			return s
		}
	}
	if level == agentAll {
		return s
	}
	return 0
}

// levelRank orders an area's levels: off lowest, all highest.
func levelRank(r areaRole, level string) int {
	switch level {
	case agentOff:
		return 0
	case agentAll:
		return len(r.levels) + 1
	}
	return slices.IndexFunc(r.levels, func(l roleLevel) bool { return l.name == level }) + 1
}

// askAreas are the area roles whose changes can ask first: those made
// inside an environment, which checkEnv decides (asksOwner,
// asksApprover). The others have nothing that asks: the Data panel's
// changes are proposals already, an incident or an alert is decided per
// item, the org's areas change nothing in an environment, and agents
// never approve. Asking is refused there rather than kept and ignored.
var askAreas = map[string]bool{"deploy": true, "variables": true, "logs": true, "domains": true, "analytics": true, "projects": true}

// checkAsk refuses asking first in an area that has nothing to ask for.
func checkAsk(s apitypes.AgentSetting) error {
	if s.Ask && !askAreas[s.Area] {
		return invalid("%s can't ask first: asking is for changes inside an environment (deploy, variables, logs, domains, analytics, projects). Lower its level instead; off keeps agents from it", s.Area)
	}
	return nil
}

// agentDefault is a level with no setting: everything, but approving.
func agentDefault(area string) string {
	if area == "approver" {
		return agentOff
	}
	return agentAll
}

// agentCap is what an agent may use of the access it acts with, per
// environment kind, what of that asks its person first, and what asks
// an approver first (the org's agent rules).
type agentCap struct {
	use, ask, ruleAsk [3]Perms
	// data is the Data panel's level per kind (data.go caps the data
	// policy's answer with it while it decides data access).
	data [3]string
}

// capOf works an agent's cap out of its person's setting (settings rows
// without a token; personal agents only), its own narrowing (rows with
// one) and the org's agent rules: the lowest level of the three, and
// asking where any asks.
func capOf(settings []store.AgentSetting, rules []store.AgentRule, personal bool) agentCap {
	var c agentCap
	for _, r := range agentAreas() {
		for i, k := range agentKinds {
			level, ask, ruleAsk := agentAll, false, false
			if personal {
				level = agentDefault(r.key)
			}
			lower := func(l string) {
				if levelRank(r, l) < levelRank(r, level) {
					level = l
				}
			}
			for _, row := range settings {
				if row.Area == r.key && uint8(row.Kind) == k {
					if row.TokenID == nil {
						level = row.Level
					}
					ask = ask || row.Ask && askAreas[r.key]
				}
			}
			for _, row := range settings {
				if row.Area == r.key && uint8(row.Kind) == k && row.TokenID != nil {
					lower(row.Level)
				}
			}
			for _, row := range rules {
				if row.Area == r.key && uint8(row.Kind) == k {
					lower(row.Level)
					ruleAsk = row.Ask && askAreas[r.key]
				}
			}
			held := levelPerms(r, level)
			c.use[i] |= held
			if r.key == "data" {
				c.data[i] = level
			}
			if ask {
				c.ask[i] |= held &^ readPerms()
			}
			if ruleAsk {
				c.ruleAsk[i] |= held &^ readPerms()
			}
		}
	}
	return c
}

// readPerms are the permissions that only read: asking first is for
// changes.
func readPerms() Perms {
	var s Perms
	for p := range permCount {
		if permTable[p].read {
			s |= 1 << p
		}
	}
	return s
}

// envPerms are the permissions decided per environment, which an agent's
// cap gives per kind; the org's and a project's are capped by what it may
// use in every kind.
func envPerms() Perms {
	var s Perms
	for p := range permCount {
		if permTable[p].level == PermEnv {
			s |= 1 << p
		}
	}
	return s
}

// capEntries is a person's grants as their agent may use them: each
// environment permission in the kinds the cap gives it, each org and
// project permission where the cap gives it in every kind.
func (c agentCap) capEntries(entries []grantEntry) []grantEntry {
	every := c.use[0] & c.use[1] & c.use[2]
	env := envPerms()
	out := make([]grantEntry, 0, len(entries)*2)
	for _, g := range entries {
		if p := g.perms &^ env & every; p != 0 {
			out = append(out, grantEntry{project: g.project, env: g.env, kinds: g.kinds, perms: p})
		}
		for i, k := range agentKinds {
			if g.kinds&k == 0 {
				continue
			}
			if p := g.perms & env & c.use[i]; p != 0 {
				out = append(out, grantEntry{project: g.project, env: g.env, kinds: k, perms: p})
			}
		}
	}
	return out
}

// isPersonalAgent reports whether the actor is a token acting for a
// person in an org: a `dock login`.
func (a Actor) isPersonalAgent() bool {
	return a.Kind == "token" && a.UserID != 0 && !a.platformOnly
}

// asksOwner reports whether a personal agent's change in e waits for its
// person: the check's permission is one they asked to approve first in
// e's kind, and neither they nor an approver approved this call.
func (a Actor) asksOwner(need apitypes.Scope, action string, e store.Environment) bool {
	return a.agent != nil && a.ownerApproved != e.ID && a.asks(a.agent.ask, need, action, e)
}

// asksApprover reports whether an agent's change in e waits for an
// approver: the org's agent rules ask first for its permission there.
func (a Actor) asksApprover(need apitypes.Scope, action string, e store.Environment) bool {
	return a.agent != nil && a.asks(a.agent.ruleAsk, need, action, e)
}

// asks reports whether a check's permission in e is in an ask set, and no
// approver's decision lets this call through.
func (a Actor) asks(set [3]Perms, need apitypes.Scope, action string, e store.Environment) bool {
	// Only while grants decide: in shadow mode the role still does.
	if !a.hasGrants || set == ([3]Perms{}) || a.granted == e.ID {
		return false
	}
	p, ok := permFor(actionKey{need, action})
	i := kindIndex(envKind(e))
	return ok && i >= 0 && set[i].has(p)
}

// everyKind is what an agent's entries give on the whole org in every
// environment kind: its cap splits them by kind (capEntries), so the
// summary role counts them here.
func everyKind(entries []grantEntry) Perms {
	var per [3]Perms
	for _, g := range entries {
		if g.project != 0 || g.env != 0 {
			continue
		}
		for i, k := range agentKinds {
			if g.kinds&k != 0 {
				per[i] |= g.perms
			}
		}
	}
	return per[0] & per[1] & per[2]
}

// capAgentData lowers a data permission to a personal agent's Data panel
// level in that kind (agentCap.data): the data policy still decides data
// access (ADR 0080, until it moves into grants), and the agent may use
// no more of it than its person allows. Below full, sensitive columns
// stay masked.
func capAgentData(perm apitypes.DataPermission, level string) apitypes.DataPermission {
	if level == agentAll || level == "full" || level == "" {
		return perm
	}
	limit := apitypes.LevelNone
	if level != agentOff {
		limit = apitypes.DataLevel(level)
	}
	if perm.Level.Min(limit) == perm.Level && perm.Sensitive != apitypes.SensitiveVisible {
		return perm
	}
	perm.Level, perm.Approve = perm.Level.Min(limit), false
	if perm.Sensitive == apitypes.SensitiveVisible {
		perm.Sensitive = apitypes.SensitiveMasked
	}
	perm.Why = append(perm.Why, apitypes.DataPermissionStep{
		Source: apitypes.PermissionCap, Level: perm.Level, Sensitive: perm.Sensitive,
		Detail: "the agent setting or the organization's agent rules let agents use the Data panel at " + level + " here",
	})
	return perm
}
