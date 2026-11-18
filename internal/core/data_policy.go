package core

import (
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The data access policy (ADR 0016 section 4) as Dock applies it: rules at
// the default, project and environment scopes, decided by datapanel.Decide.
// Admins always have full access; tokens follow the tokens rule or their
// own override, capped by what users of the token's scope and its
// creator's current role get, and only propose on production.

// dockBuiltIn is Dock's built-in default for a subject and kind: deploy
// users read non-production, nobody else gets anything, sensitive columns
// masked.
func dockBuiltIn(subject apitypes.DataSubject, kind apitypes.EnvironmentKind) apitypes.DataPolicyRule {
	level := apitypes.LevelNone
	if subject == apitypes.SubjectDeployUsers && kind == apitypes.KindNonProduction {
		level = apitypes.LevelRead
	}
	return apitypes.DataPolicyRule{Subject: subject, Kind: kind, Level: level, Sensitive: apitypes.SensitiveMasked, From: apitypes.PolicyBuiltIn}
}

// policyRules are the rules that can apply in one project or environment
// (ListEffectiveDataPolicyRules: the default's, the project's and the
// environment's).
type policyRules []store.DataPolicyRule

// scoped converts the rules, From saying where each was set.
func (rs policyRules) scoped() []apitypes.DataPolicyRule {
	out := make([]apitypes.DataPolicyRule, len(rs))
	for i, r := range rs {
		out[i] = toPolicyRule(r)
		switch {
		case r.EnvironmentID != nil:
			out[i].From = apitypes.PolicyEnvironment
		case r.ProjectID != nil:
			out[i].From = apitypes.PolicyProject
		default:
			out[i].From = apitypes.PolicyDefault
		}
	}
	return out
}

func toPolicyRule(r store.DataPolicyRule) apitypes.DataPolicyRule {
	return apitypes.DataPolicyRule{
		Subject: apitypes.DataSubject(r.Subject), Kind: apitypes.EnvironmentKind(r.Kind),
		Level: apitypes.DataLevel(r.Level), Sensitive: apitypes.DataSensitivity(r.Sensitive),
	}
}

// subjectOf is the policy subject for users of a role; admins have none.
func subjectOf(role apitypes.Scope) apitypes.DataSubject {
	if role == apitypes.ScopeDeploy {
		return apitypes.SubjectDeployUsers
	}
	return apitypes.SubjectReadUsers
}

// permissionInput is everything that decides a caller's data permission in
// one environment; dataPermission loads it.
type permissionInput struct {
	actor Actor
	// token is the actor's token row (tokens below admin only).
	token *store.Token
	// creator is the current role of the user who created the token; nil
	// when the creator isn't a user (another token, the system).
	creator *apitypes.Scope
	// creatorGone: the user who created the token is no longer a member
	// of the org, so the token gets no data access (nobody's role stands
	// behind it) until an admin makes a new one.
	creatorGone bool
	rules       policyRules
	kind        apitypes.EnvironmentKind
	// platform is the platform's default and ceiling (nil: none).
	platform *platformPolicy
	// project and env name the environment, for the explanation.
	project, env string
}

// decidePermission works out what the caller may do and why
// (datapanel.Decide) from Dock's view of them. A `dock login` token acts
// for its user (isAgent), so it is judged like that user (its subject is
// their role, dataScope) and may write production like them, as far as
// their agent setting lets it (dataPermission); every other token is an
// agent: the tokens rule or its own
// override, capped by its scope and creator, and only proposing on
// production.
func decidePermission(in permissionInput) apitypes.DataPermission {
	a := in.actor
	agent, scope := isAgent(a), a.dataScope()
	pin := datapanel.PolicyInput{
		Kind: in.kind, Admin: rank(scope) >= rank(apitypes.ScopeAdmin), Agent: agent, Subject: subjectOf(scope),
		Rules: in.rules.scoped(), BuiltIn: in.platform.builtIn,
		ScopeName: func(s apitypes.DataPolicyScope) string {
			switch s {
			case apitypes.PolicyProject:
				return "project " + in.project
			case apitypes.PolicyEnvironment:
				return "environment " + in.project + "/" + in.env
			}
			return ""
		},
	}
	if a.Kind == "token" {
		tok := &datapanel.TokenInput{Name: a.Name}
		if in.token != nil {
			tok.Override = tokenData(*in.token)
		}
		if agent {
			tok.Caps = append(tok.Caps, datapanel.Cap{Subject: subjectOf(scope), Why: fmt.Sprintf("capped by the token's scope %s: ", scope)})
		}
		who := "its creator "
		if !agent {
			who = "its user "
		}
		if in.token != nil {
			who += in.token.CreatedBy
		}
		if in.creatorGone {
			tok.Caps = append(tok.Caps, datapanel.Cap{None: true, Why: fmt.Sprintf("%s is no longer a member of the organization, so the token gets no data access; an admin makes a new token", who)})
		} else if in.creator != nil && rank(*in.creator) < rank(apitypes.ScopeAdmin) {
			tok.Caps = append(tok.Caps, datapanel.Cap{Subject: subjectOf(*in.creator), Why: fmt.Sprintf("capped by %s's current role %s: ", who, *in.creator)})
		}
		pin.Token = tok
	}
	subject := subjectOf(scope)
	if agent {
		subject = apitypes.SubjectTokens
	}
	return in.platform.capPermission(datapanel.Decide(pin), pin.Admin, subject, in.kind)
}

// dataPermission loads what decides the actor's data permission in an
// environment (at most three queries: the token, its creator and the
// rules) and decides it. Admins need none.
func (c *Core) dataPermission(ctx context.Context, a Actor, es envScope) (apitypes.DataPermission, error) {
	in := permissionInput{actor: a, kind: apitypes.EnvironmentKind(es.env.Kind), project: es.project.Name, env: es.env.Name, platform: c.platformPolicy.Load()}
	if rank(a.dataScope()) < rank(apitypes.ScopeAdmin) {
		if a.Kind == "token" {
			t, err := c.store.GetToken(ctx, a.TokenID)
			if err != nil {
				return apitypes.DataPermission{}, fmt.Errorf("load token %d: %w", a.TokenID, err)
			}
			in.token = &t
			// The creator is the user the token records by ID, never
			// whoever holds the name it was created under now. A token
			// without one was made by another token or the system:
			// deleting a user revokes the tokens they made.
			if t.UserID != nil {
				r, err := c.store.GetMemberRole(ctx, store.GetMemberRoleParams{OrgID: es.project.OrgID, UserID: *t.UserID})
				switch {
				case err == nil:
					role := apitypes.Scope(r)
					in.creator = &role
				case store.IsNotFound(err):
					in.creatorGone = true
				default:
					return apitypes.DataPermission{}, err
				}
			}
		}
		rules, err := c.store.ListEffectiveDataPolicyRules(ctx, store.ListEffectiveDataPolicyRulesParams{OrgID: es.project.OrgID, ProjectID: es.project.ID, EnvironmentID: es.env.ID})
		if err != nil {
			return apitypes.DataPermission{}, err
		}
		in.rules = rules
	}
	perm := decidePermission(in)
	shadowData(in, perm)
	if a.lockedOut(es.env) {
		perm = lockedData(perm, es)
	}
	if a.hasGrants && a.agent != nil && a.agent.data != ([3]string{}) {
		if i := kindIndex(envKind(es.env)); i >= 0 {
			perm = capAgentData(perm, a.agent.data[i])
		}
	}
	if a.hasGrants && a.isPersonalAgent() && perm.Approve && !a.approves(es.project.ID, es.env.ID, envKind(es.env)) {
		// Judged as its person, an admin's agent would approve changesets;
		// agents approve only where their Approver setting says.
		perm.Approve = false
	}
	if a.suspended {
		// ADR 0062: in a suspended org the actor reads, nothing more.
		perm.Level, perm.Approve, perm.Manage = perm.Level.Min(apitypes.LevelRead), false, false
		perm.Why = append(perm.Why, apitypes.DataPermissionStep{
			Source: apitypes.PermissionCap, Level: perm.Level, Sensitive: perm.Sensitive,
			Detail: fmt.Sprintf("organization %s is suspended by the platform's operator: data reads only until it is unsuspended", a.OrgSlug),
		})
	}
	return perm, nil
}

// DataLevel is the actor's data access level in an environment, for a
// caller that tailors what it offers to it (the MCP tool list); the data
// operations decide permission themselves.
func (c *Core) DataLevel(ctx context.Context, a Actor, ref EnvRef) (apitypes.DataLevel, error) {
	es, err := c.envFor(ctx, a, ref, apitypes.ScopeRead, "reading data access")
	if err != nil {
		return "", err
	}
	perm, err := c.dataPermission(ctx, a, es)
	return perm.Level, err
}

// lockedData caps a token's data permission in a locked environment (ADR
// 0044) at proposing: its changesets wait for an admin's approval in the
// Data panel, which is the access request for data.
func lockedData(perm apitypes.DataPermission, es envScope) apitypes.DataPermission {
	perm.Level, perm.Approve, perm.Manage = perm.Level.Min(apitypes.LevelPropose), false, false
	perm.Why = append(perm.Why, apitypes.DataPermissionStep{
		Source: apitypes.PermissionLocked, Level: perm.Level, Sensitive: perm.Sensitive,
		Detail: fmt.Sprintf("environment %s is locked: tokens propose changesets an admin approves", es),
	})
	return perm
}

// isAgent reports whether the actor is a token in its own right. A `dock
// login` token (AuthenticateToken sets its Email) acts for its user.
func isAgent(a Actor) bool { return a.Kind == "token" && a.Email == "" }

// policyScope resolves a policy reference (see GetDataPolicy) and checks
// the actor's scope there. Changing the default policy needs an unlimited
// admin.
func (c *Core) policyScope(ctx context.Context, a Actor, ref EnvRef, need apitypes.Scope, action string) (apitypes.DataPolicy, *store.Project, *store.Environment, error) {
	switch {
	case ref.Project == "" && ref.Environment != "":
		return apitypes.DataPolicy{}, nil, nil, invalid("an environment's data policy needs its project too")
	case ref.Environment != "":
		es, err := c.envFor(ctx, a, ref, need, action)
		if err != nil {
			return apitypes.DataPolicy{}, nil, nil, err
		}
		return apitypes.DataPolicy{Scope: apitypes.PolicyEnvironment, Project: es.project.Name, Environment: es.env.Name}, &es.project, &es.env, nil
	case ref.Project != "":
		p, err := c.projectFor(ctx, a, ref.Project, need, action)
		if err != nil {
			return apitypes.DataPolicy{}, nil, nil, err
		}
		return apitypes.DataPolicy{Scope: apitypes.PolicyProject, Project: p.Name}, &p, nil, nil
	}
	if need == apitypes.ScopeAdmin {
		return apitypes.DataPolicy{Scope: apitypes.PolicyDefault}, nil, nil, a.requireUnlimited(need, action)
	}
	return apitypes.DataPolicy{Scope: apitypes.PolicyDefault}, nil, nil, a.require(need, action)
}

// scopeIDs are a policy scope's project and environment IDs, 0 for none.
func scopeIDs(p *store.Project, e *store.Environment) (int64, int64) {
	var pid, eid int64
	if p != nil {
		pid = p.ID
	}
	if e != nil {
		eid = e.ID
	}
	return pid, eid
}

// GetDataPolicy returns the data access policy at one scope: the default
// for an empty ref, a project's for ref.Project alone, an environment's
// for both. Anyone who can read the scope may read its policy.
func (c *Core) GetDataPolicy(ctx context.Context, a Actor, ref EnvRef) (apitypes.DataPolicy, error) {
	pol, p, e, err := c.policyScope(ctx, a, ref, apitypes.ScopeRead, "reading the data access policy")
	if err != nil {
		return pol, err
	}
	return c.loadPolicy(ctx, a.OrgID, pol, p, e)
}

// loadPolicy fills a policy's rules and effective rules in an org (two
// queries).
func (c *Core) loadPolicy(ctx context.Context, orgID int64, pol apitypes.DataPolicy, p *store.Project, e *store.Environment) (apitypes.DataPolicy, error) {
	pid, eid := scopeIDs(p, e)
	own, err := c.store.ListDataPolicyRules(ctx, store.ListDataPolicyRulesParams{OrgID: orgID, ProjectID: pid, EnvironmentID: eid})
	if err != nil {
		return pol, err
	}
	all := policyRules(own)
	if p != nil {
		if all, err = c.store.ListEffectiveDataPolicyRules(ctx, store.ListEffectiveDataPolicyRulesParams{OrgID: orgID, ProjectID: pid, EnvironmentID: eid}); err != nil {
			return pol, err
		}
	}
	pol.Rules = make([]apitypes.DataPolicyRule, len(own))
	for i, r := range own {
		pol.Rules[i] = toPolicyRule(r)
	}
	kinds := datapanel.Kinds
	if e != nil {
		kinds = []apitypes.EnvironmentKind{apitypes.EnvironmentKind(e.Kind)}
	}
	pol.Effective = datapanel.Effective(all.scoped(), kinds, c.platformPolicy.Load().builtIn)
	return pol, nil
}

// SetDataPolicy replaces the rules at one scope (see GetDataPolicy).
// Admin only; the org's default policy needs an unlimited admin. An
// environment's rules may only name its own kind.
func (c *Core) SetDataPolicy(ctx context.Context, a Actor, ref EnvRef, req apitypes.UpdateDataPolicyRequest) (apitypes.DataPolicy, error) {
	pol, p, e, err := c.policyScope(ctx, a, ref, apitypes.ScopeAdmin, "changing the data access policy")
	if err != nil {
		return pol, err
	}
	var only apitypes.EnvironmentKind
	scope := ""
	if e != nil {
		only, scope = apitypes.EnvironmentKind(e.Kind), "environment "+p.Name+"/"+e.Name
	}
	if err := datapanel.ValidateRules(req.Rules, only, scope); err != nil {
		return pol, err
	}
	if err := c.checkCeiling(req.Rules); err != nil {
		return pol, err
	}
	pid, eid := scopeIDs(p, e)
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteDataPolicyRules(ctx, store.DeleteDataPolicyRulesParams{OrgID: a.OrgID, ProjectID: pid, EnvironmentID: eid}); err != nil {
			return err
		}
		for _, r := range req.Rules {
			params := store.AddDataPolicyRuleParams{OrgID: a.OrgID, Subject: string(r.Subject), Kind: string(r.Kind), Level: string(r.Level), Sensitive: string(r.Sensitive)}
			if p != nil {
				params.ProjectID = &p.ID
			}
			if e != nil {
				params.EnvironmentID = &e.ID
			}
			if err := q.AddDataPolicyRule(ctx, params); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return pol, err
	}
	target := "default data policy"
	switch {
	case e != nil:
		target = p.Name + "/" + e.Name
	case p != nil:
		target = p.Name
	}
	c.audit(ctx, a, apitypes.AuditDataPolicy, p, e, target, datapanel.DescribeRules(target, req.Rules))
	return c.loadPolicy(ctx, a.OrgID, pol, p, e)
}

// tokenData is a token's own data access, nil when it follows the policy.
// A missing part means none (levels) or masked.
func tokenData(t store.Token) *apitypes.DataTokenAccess {
	if t.DataProduction == nil && t.DataNonProduction == nil && t.DataSensitive == nil {
		return nil
	}
	d := apitypes.DataTokenAccess{Production: apitypes.LevelNone, NonProduction: apitypes.LevelNone, Sensitive: apitypes.SensitiveMasked}
	if t.DataProduction != nil {
		d.Production = apitypes.DataLevel(*t.DataProduction)
	}
	if t.DataNonProduction != nil {
		d.NonProduction = apitypes.DataLevel(*t.DataNonProduction)
	}
	if t.DataSensitive != nil {
		d.Sensitive = apitypes.DataSensitivity(*t.DataSensitive)
	}
	return &d
}

// tokenDataParams validates a token data override and turns it into store
// parameters; nil clears it.
func tokenDataParams(id int64, d *apitypes.DataTokenAccess) (store.SetTokenDataAccessParams, error) {
	params := store.SetTokenDataAccessParams{ID: id}
	if d == nil {
		return params, nil
	}
	if err := datapanel.ValidateTokenAccess(d); err != nil {
		return params, err
	}
	prod, nonProd, sens := string(d.Production), string(d.NonProduction), string(d.Sensitive)
	params.DataProduction, params.DataNonProduction, params.DataSensitive = &prod, &nonProd, &sens
	return params, nil
}

// UpdateToken changes the data access override of one of the org's tokens
// (orgToken: another org's token is not found); null follows the
// policy again. Admin only (unlimited). The override is stored as given
// and capped when it is used, by what users of the token's scope and its
// creator's current role get then.
func (c *Core) UpdateToken(ctx context.Context, a Actor, id int64, req apitypes.UpdateTokenRequest) (apitypes.Token, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing tokens"); err != nil {
		return apitypes.Token{}, err
	}
	t, err := c.orgToken(ctx, a, id)
	if err != nil {
		return apitypes.Token{}, err
	}
	if t.RevokedAt != nil {
		return apitypes.Token{}, invalid("token %q is revoked; create a new one", t.Name)
	}
	params, err := tokenDataParams(id, req.Data)
	if err != nil {
		return apitypes.Token{}, err
	}
	if _, err := c.store.SetTokenDataAccess(ctx, params); err != nil {
		return apitypes.Token{}, err
	}
	t.DataProduction, t.DataNonProduction, t.DataSensitive = params.DataProduction, params.DataNonProduction, params.DataSensitive
	c.audit(ctx, a, apitypes.AuditDataPolicy, nil, nil, "token "+t.Name, fmt.Sprintf("token %q (id %d) %s", t.Name, t.ID, datapanel.DescribeTokenAccess(req.Data)))
	return toToken(ctx, newNames(c.store), t), nil
}
