package tempobase

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// The data access policy, mapped to tempoBase (ADR 0024 section 2):
// scopes default and connection; a connection's production flag is its
// kind; viewers are subject role:read and editors role:deploy; admins can
// do everything. Tokens are agents with no scope of their own: the tokens
// rule or their own access, capped by their owner's current role unless
// the owner is an admin. A connection where a caller's level is none is
// invisible to them.

// builtIn is tempoBase's default when no rule is set: viewers read,
// editors write (production commits confirm the connection's name),
// tokens get nothing until granted; sensitive columns masked.
func builtIn(subject apitypes.DataSubject, kind apitypes.EnvironmentKind) apitypes.DataPolicyRule {
	level := apitypes.LevelNone
	switch subject {
	case apitypes.SubjectReadUsers:
		level = apitypes.LevelRead
	case apitypes.SubjectDeployUsers:
		level = apitypes.LevelWrite
	}
	return apitypes.DataPolicyRule{Subject: subject, Kind: kind, Level: level, Sensitive: apitypes.SensitiveMasked, From: apitypes.PolicyBuiltIn}
}

// subjectOf is the policy subject of a role; admins have none.
func subjectOf(r tbtypes.Role) apitypes.DataSubject {
	if r == tbtypes.RoleEditor {
		return apitypes.SubjectDeployUsers
	}
	return apitypes.SubjectReadUsers
}

// kindOf is a connection's environment kind.
func kindOf(c tbstore.Connection) apitypes.EnvironmentKind {
	if c.Production != 0 {
		return apitypes.KindProduction
	}
	return apitypes.KindNonProduction
}

// toRule converts a stored rule, with From saying where it was set.
func toRule(r tbstore.DataPolicyRule) apitypes.DataPolicyRule {
	from := apitypes.PolicyDefault
	if r.ConnectionID != nil {
		from = policyConnection
	}
	return apitypes.DataPolicyRule{
		Subject: apitypes.DataSubject(r.Subject), Kind: apitypes.EnvironmentKind(r.Kind),
		Level: apitypes.DataLevel(r.Level), Sensitive: apitypes.DataSensitivity(r.Sensitive), From: from,
	}
}

// rulesFor are the rules that can apply on connection id: the default's
// and its own, from every rule.
func rulesFor(all []tbstore.DataPolicyRule, id int64) []apitypes.DataPolicyRule {
	out := make([]apitypes.DataPolicyRule, 0, len(all))
	for _, r := range all {
		if r.ConnectionID == nil || *r.ConnectionID == id {
			out = append(out, toRule(r))
		}
	}
	return out
}

// permission decides act's data permission on c from the rules that apply
// there (rulesFor). Pure: the actor carries its token and owner role.
func permission(act Actor, c tbstore.Connection, rules []apitypes.DataPolicyRule) apitypes.DataPermission {
	in := datapanel.PolicyInput{
		Kind: kindOf(c), Admin: act.IsAdmin(), Subject: subjectOf(act.Role), Rules: rules, BuiltIn: builtIn,
		ScopeName: func(apitypes.DataPolicyScope) string { return "connection " + c.Name },
	}
	if act.Kind == "token" {
		in.Agent = true
		in.Token = &datapanel.TokenInput{Name: act.Name, Override: act.TokenData}
		if act.Role != tbtypes.RoleAdmin {
			in.Token.Caps = []datapanel.Cap{{Subject: subjectOf(act.Role), Why: "capped by its owner " + act.Email + "'s current role " + string(act.Role) + ": "}}
		}
	}
	return decide(in)
}

// visible reports whether act may see a connection where its permission
// is perm: admins see everything; others need a level above none, and a
// token's connection limit must include it.
func visible(act Actor, c tbstore.Connection, perm apitypes.DataPermission) bool {
	if act.IsAdmin() {
		return true
	}
	return perm.Level != apitypes.LevelNone && act.mayUse(c.ID)
}

// connFor loads the connection named name and act's permission there (two
// queries). A connection act may not see is not found.
func (a *App) connFor(ctx context.Context, act Actor, name string) (tbstore.Connection, apitypes.DataPermission, error) {
	c, err := a.store.GetConnectionByName(ctx, name)
	if tbstore.IsNotFound(err) {
		return c, apitypes.DataPermission{}, notFound("connection %q not found", name)
	}
	if err != nil {
		return c, apitypes.DataPermission{}, err
	}
	rows, err := a.store.ListEffectivePolicyRules(ctx, c.ID)
	if err != nil {
		return c, apitypes.DataPermission{}, err
	}
	perm := permission(act, c, rulesFor(rows, c.ID))
	if !visible(act, c, perm) {
		return c, perm, notFound("connection %q not found", name)
	}
	return c, perm, nil
}

// GetDataPolicy returns the data access policy at one scope: the default
// for connection "", else the connection's. Anyone may read the default;
// a connection's needs seeing it.
func (a *App) GetDataPolicy(ctx context.Context, act Actor, connection string) (apitypes.DataPolicy, error) {
	pol, c, err := a.policyScope(ctx, act, connection)
	if err != nil {
		return pol, err
	}
	return a.loadPolicy(ctx, pol, c)
}

// policyScope resolves a policy's scope: nil connection for the default.
func (a *App) policyScope(ctx context.Context, act Actor, connection string) (apitypes.DataPolicy, *tbstore.Connection, error) {
	if connection == "" {
		return apitypes.DataPolicy{Scope: apitypes.PolicyDefault}, nil, nil
	}
	c, _, err := a.connFor(ctx, act, connection)
	if err != nil {
		return apitypes.DataPolicy{}, nil, err
	}
	return apitypes.DataPolicy{Scope: policyConnection}, &c, nil
}

// loadPolicy fills a policy's own and effective rules (one or two
// queries).
func (a *App) loadPolicy(ctx context.Context, pol apitypes.DataPolicy, c *tbstore.Connection) (apitypes.DataPolicy, error) {
	var id int64
	kinds := envKinds
	if c != nil {
		id, kinds = c.ID, []apitypes.EnvironmentKind{kindOf(*c)}
	}
	own, err := a.store.ListPolicyRules(ctx, id)
	if err != nil {
		return pol, err
	}
	all := own
	if c != nil {
		if all, err = a.store.ListEffectivePolicyRules(ctx, id); err != nil {
			return pol, err
		}
	}
	pol.Rules = make([]apitypes.DataPolicyRule, len(own))
	for i, r := range own {
		pol.Rules[i] = toRule(r)
		pol.Rules[i].From = ""
	}
	pol.Effective = effectiveRules(rulesFor(all, id), kinds, builtIn)
	return pol, nil
}

// SetDataPolicy replaces the rules at one scope (see GetDataPolicy).
// Admin only. A connection's rules may only name its kind.
func (a *App) SetDataPolicy(ctx context.Context, act Actor, connection string, req apitypes.UpdateDataPolicyRequest) (apitypes.DataPolicy, error) {
	if err := act.requireAdmin("changing the data access policy"); err != nil {
		return apitypes.DataPolicy{}, err
	}
	pol, c, err := a.policyScope(ctx, act, connection)
	if err != nil {
		return pol, err
	}
	var (
		only   apitypes.EnvironmentKind
		id     *int64
		target = "the default data policy"
	)
	if c != nil {
		only, id, target = kindOf(*c), &c.ID, "connection "+c.Name
	}
	if err := validateRules(req.Rules, only, target); err != nil {
		return pol, err
	}
	err = a.store.Tx(ctx, func(q *tbstore.Queries) error {
		var scope int64
		if id != nil {
			scope = *id
		}
		if err := q.DeletePolicyRules(ctx, scope); err != nil {
			return err
		}
		for _, r := range req.Rules {
			if err := q.AddPolicyRule(ctx, tbstore.AddPolicyRuleParams{
				ConnectionID: id, Subject: string(r.Subject), Kind: string(r.Kind), Level: string(r.Level), Sensitive: string(r.Sensitive),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return pol, err
	}
	a.audit(ctx, act, apitypes.AuditDataPolicy, id, target, describeRules(target, req.Rules))
	return a.loadPolicy(ctx, pol, c)
}
