package core

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Org agents (ADR 0080, phase 6b): an organization's tokens hold grants
// and join groups like people. A token's scope and limit become grants
// when it is made (older ones at start): its scope's seeded roles at its
// limit's place, so it does exactly what it did; admins then add or end
// grants and put it in groups.

// grantToken gives a token its scope's roles at its limit's place, in
// q's transaction, and marks it granted.
func grantToken(ctx context.Context, q *store.Queries, t store.Token) error {
	if t.OrgID == nil {
		return nil
	}
	for _, r := range scopeRoles(apitypes.Scope(t.Scope)) {
		if _, err := q.CreateGrant(ctx, store.CreateGrantParams{
			OrgID: *t.OrgID, TokenID: &t.ID, Role: r, ProjectID: t.ProjectID, EnvironmentID: t.EnvironmentID,
			Kinds: allKinds, Via: ptr("token"), GrantedBy: "system",
		}); err != nil {
			return fmt.Errorf("grant token %d %s: %w", t.ID, r, err)
		}
	}
	return q.SetTokenGranted(ctx, t.ID)
}

// grantTokens turns the scope and limit of org tokens from before org
// agents into grants, once, at start.
func (c *Core) grantTokens(ctx context.Context) error {
	tokens, err := c.store.ListTokensToGrant(ctx)
	if err != nil {
		return err
	}
	for _, t := range tokens {
		if err := c.store.Tx(ctx, func(q *store.Queries) error { return grantToken(ctx, q, t) }); err != nil {
			return err
		}
	}
	return nil
}

// loadTokenAccess works out what an org token holds: its scope's roles at
// its limit until an admin changes its grants or groups (own_grants), then
// what it holds (one query); capped by the org's agent rules unless it
// bypasses them (orgRules, kept in memory); and makes it
// its access while grants decide; in shadow mode the shadow answers from
// it. Its Scope stays the token's own, for what still speaks in scopes
// (the data policy).
func (c *Core) loadTokenAccess(ctx context.Context, a Actor, t store.Token) (Actor, error) {
	var entries []grantEntry
	if t.OwnGrants {
		rows, err := c.store.ListTokenGrants(ctx, store.ListTokenGrantsParams{TokenID: &a.TokenID, Now: ptr(now().Unix())})
		if err != nil {
			return a, err
		}
		entries = make([]grantEntry, 0, len(rows))
		for _, r := range rows {
			if perms, ok := grantPerms(r.Role, r.Parts); ok {
				entries = append(entries, grantEntry{project: deref(r.ProjectID), env: deref(r.EnvironmentID), kinds: uint8(r.Kinds), perms: perms})
			}
		}
	} else {
		// Its grants are still its scope's at its limit (grantToken):
		// the same, without reading them.
		entries = []grantEntry{{project: a.ProjectID, env: a.EnvironmentID, kinds: uint8(allKinds), perms: scopePerms(apitypes.Scope(t.Scope))}}
	}
	if !t.RulesBypass {
		// The org's agent rules cap it.
		rules, err := c.orgRules(ctx, a.OrgID)
		if err != nil {
			return a, err
		}
		if len(rules) > 0 {
			cp := capOf(nil, rules, false)
			entries, a.agent = cp.capEntries(entries), &cp
		}
	}
	a.grants = entries
	if c.enforceGrants.Load() {
		a.hasGrants = true
	} else {
		a.watchGrants = true
	}
	return a, nil
}

// agentToken loads one of the org's tokens by name or ID, for an admin
// managing it.
func (c *Core) agentToken(ctx context.Context, a Actor, ref string) (store.Token, error) {
	if err := a.require(apitypes.ScopeAdmin, "managing tokens"); err != nil {
		return store.Token{}, err
	}
	var t store.Token
	var err error
	if id, perr := strconv.ParseInt(ref, 10, 64); perr == nil {
		t, err = c.store.GetToken(ctx, id)
		if err == nil && (t.OrgID == nil || *t.OrgID != a.OrgID || t.Origin != "manual" || t.RevokedAt != nil) {
			err = sql.ErrNoRows
		}
	} else {
		t, err = c.store.GetOrgTokenByName(ctx, store.GetOrgTokenByNameParams{OrgID: &a.OrgID, Name: ref})
	}
	if store.IsNotFound(err) {
		return t, notFound("token %q not found in %s; `dock token list` lists the organization's", ref, a.OrgSlug)
	}
	return t, err
}

// AgentAccess is an org agent's grants and groups.
func (c *Core) AgentAccess(ctx context.Context, a Actor, ref string) (apitypes.AgentAccess, error) {
	t, err := c.agentToken(ctx, a, ref)
	if err != nil {
		return apitypes.AgentAccess{}, err
	}
	grants, err := c.store.ListOrgGrants(ctx, store.ListOrgGrantsParams{OrgID: a.OrgID, Now: ptr(now().Unix())})
	if err != nil {
		return apitypes.AgentAccess{}, err
	}
	agents, err := c.store.ListGroupAgents(ctx, a.OrgID)
	if err != nil {
		return apitypes.AgentAccess{}, err
	}
	groups, err := c.store.ListGroups(ctx, a.OrgID)
	if err != nil {
		return apitypes.AgentAccess{}, err
	}
	out := apitypes.AgentAccess{TokenID: t.ID, Name: t.Name, RulesBypass: t.RulesBypass, Groups: []string{}, Grants: []apitypes.Grant{}}
	for _, g := range grants {
		if g.TokenID != nil && *g.TokenID == t.ID {
			out.Grants = append(out.Grants, toGrant(g))
		}
	}
	for _, ag := range agents {
		if ag.TokenID != t.ID {
			continue
		}
		for _, g := range groups {
			if g.ID == ag.GroupID {
				out.Groups = append(out.Groups, g.Name)
			}
		}
	}
	return out, nil
}

// GrantAgent gives an org agent a role at a place, beside its groups'.
func (c *Core) GrantAgent(ctx context.Context, a Actor, ref string, req apitypes.GrantRequest) (apitypes.AgentAccess, error) {
	t, err := c.agentToken(ctx, a, ref)
	if err != nil {
		return apitypes.AgentAccess{}, err
	}
	if err := a.requireRecent("managing tokens"); err != nil {
		return apitypes.AgentAccess{}, err
	}
	p, place, err := c.grantParams(ctx, a, req)
	if err != nil {
		return apitypes.AgentAccess{}, err
	}
	p.TokenID = &t.ID
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if _, err := q.CreateGrant(ctx, p); err != nil {
			return err
		}
		return q.SetTokenOwnGrants(ctx, t.ID)
	})
	if err != nil {
		return apitypes.AgentAccess{}, err
	}
	c.audit(ctx, a, "agent.grant", nil, nil, "token "+t.Name, fmt.Sprintf("gave token %s %s on %s", t.Name, req.Role, place))
	return c.AgentAccess(ctx, a, strconv.FormatInt(t.ID, 10))
}

// SetGroupAgent puts an org agent in a group, or takes it out.
func (c *Core) SetGroupAgent(ctx context.Context, a Actor, groupRef, tokenRef string, in bool) (apitypes.Group, error) {
	g, err := c.groupFor(ctx, a, groupRef, "managing groups")
	if err != nil {
		return apitypes.Group{}, err
	}
	t, err := c.agentToken(ctx, a, tokenRef)
	if err != nil {
		return apitypes.Group{}, err
	}
	if in {
		// Nobody gives more than they hold (gives.go).
		if err := c.mayGiveGroup(ctx, a, g); err != nil {
			return apitypes.Group{}, err
		}
		err = c.store.Tx(ctx, func(q *store.Queries) error {
			if err := q.AddGroupAgent(ctx, store.AddGroupAgentParams{GroupID: g.ID, TokenID: t.ID}); err != nil {
				return err
			}
			return q.SetTokenOwnGrants(ctx, t.ID)
		})
	} else {
		var n int64
		if n, err = c.store.RemoveGroupAgent(ctx, store.RemoveGroupAgentParams{GroupID: g.ID, TokenID: t.ID}); err == nil && n == 0 {
			err = notFound("token %s isn't in group %s", t.Name, g.Name)
		}
	}
	if err != nil {
		return apitypes.Group{}, err
	}
	verb, word := "group.add_agent", "put token %s in group %s"
	if !in {
		verb, word = "group.remove_agent", "took token %s out of group %s"
	}
	c.audit(ctx, a, verb, nil, nil, "group "+g.Name, fmt.Sprintf(word, t.Name, g.Name))
	return c.group(ctx, a, g.ID)
}
