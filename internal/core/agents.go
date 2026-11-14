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

// agentDefault is a level with no setting: everything, but approving.
func agentDefault(area string) string {
	if area == "approver" {
		return agentOff
	}
	return agentAll
}

// agentCap is what a personal agent may use of its person's access, per
// environment kind, and what of that it asks them first for.
type agentCap struct {
	use, ask [3]Perms
	// data is the Data panel's level per kind (data.go caps the data
	// policy's answer with it while it decides data access).
	data [3]string
}

// capOf works an agent's cap out of its person's setting (rows without a
// token) and its own narrowing (rows with one), which can only lower a
// level and add asking.
func capOf(rows []store.AgentSetting) agentCap {
	var c agentCap
	for _, r := range agentAreas() {
		for i, k := range agentKinds {
			person, narrow := agentDefault(r.key), agentAll
			ask := false
			for _, row := range rows {
				if row.Area != r.key || uint8(row.Kind) != k {
					continue
				}
				if row.TokenID == nil {
					person = row.Level
				} else {
					narrow = row.Level
				}
				ask = ask || row.Ask
			}
			level := person
			if levelRank(r, narrow) < levelRank(r, person) {
				level = narrow
			}
			held := levelPerms(r, level)
			c.use[i] |= held
			if r.key == "data" {
				c.data[i] = level
			}
			if ask {
				c.ask[i] |= held &^ readPerms()
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
// e's kind, and no approved request lets this call through.
func (a Actor) asksOwner(need apitypes.Scope, action string, e store.Environment) bool {
	// Only while grants decide: in shadow mode the role still does.
	if !a.hasGrants || a.agentAsk == ([3]Perms{}) || a.granted == e.ID {
		return false
	}
	p, ok := permFor(actionKey{need, action})
	i := kindIndex(envKind(e))
	return ok && i >= 0 && a.agentAsk[i].has(p)
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
		Detail: "your agent setting lets your agents use the Data panel at " + level + " here (Account › What your agents may do)",
	})
	return perm
}
