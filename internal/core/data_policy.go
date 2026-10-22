package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The data access policy (ADR 0016 section 4): admins set, per subject and
// environment kind, a level and sensitive-column visibility, at the
// default, project and environment scopes. Per subject and kind the most
// specific rule wins, then the built-in defaults. Admins always have full
// access; tokens follow the tokens rule or their own override, capped by
// what users of the token's scope and its creator's current role get, and
// only propose on production.

// dataSubjects and envKinds list every policy subject and environment kind,
// in the order the policy shows them.
var (
	dataSubjects = []apitypes.DataSubject{apitypes.SubjectReadUsers, apitypes.SubjectDeployUsers, apitypes.SubjectTokens}
	envKinds     = []apitypes.EnvironmentKind{apitypes.KindProduction, apitypes.KindNonProduction}
)

// builtInRule is the built-in default for a subject and kind: deploy users
// read non-production, nobody else gets anything, sensitive columns masked.
func builtInRule(subject apitypes.DataSubject, kind apitypes.EnvironmentKind) apitypes.DataPolicyRule {
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

// resolve returns the rule that applies to subject in environments of kind:
// the most specific one set, else the built-in default. From says which.
func (rs policyRules) resolve(subject apitypes.DataSubject, kind apitypes.EnvironmentKind) apitypes.DataPolicyRule {
	best, bestRank := builtInRule(subject, kind), 0
	for _, r := range rs {
		if r.Subject != string(subject) || r.Kind != string(kind) {
			continue
		}
		if from, rank := ruleScope(r); rank > bestRank {
			best, bestRank = toPolicyRule(r), rank
			best.From = from
		}
	}
	return best
}

// ruleScope says where a stored rule was set, ranked by specificity.
func ruleScope(r store.DataPolicyRule) (apitypes.DataPolicyScope, int) {
	switch {
	case r.EnvironmentID != nil:
		return apitypes.PolicyEnvironment, 3
	case r.ProjectID != nil:
		return apitypes.PolicyProject, 2
	}
	return apitypes.PolicyDefault, 1
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

// lessSensitive returns the stricter of two visibilities.
func lessSensitive(a, b apitypes.DataSensitivity) apitypes.DataSensitivity {
	if a == apitypes.SensitiveVisible && b == apitypes.SensitiveVisible {
		return apitypes.SensitiveVisible
	}
	return apitypes.SensitiveMasked
}

// permissionInput is everything that decides a caller's data permission in
// one environment; dataPermission loads it.
type permissionInput struct {
	actor Actor
	// token is the actor's token row (tokens below admin only).
	token *store.Token
	// creator is the current role of the user who created the token; nil
	// when the creator isn't a user (another token, the system) or is gone.
	creator *apitypes.Scope
	rules   policyRules
	kind    apitypes.EnvironmentKind
	// project and env name the environment, for the explanation.
	project, env string
}

// decidePermission works out what the caller may do and why, step by step
// (ADR 0016 section 4). A `dock login` token acts for its user (isAgent),
// so it is judged like that user (its subject is its role, capped by the
// user's current role) and may write production like them; every other
// token is a token: the tokens rule or its own override, capped, and only
// proposing on production.
func decidePermission(in permissionInput) apitypes.DataPermission {
	a := in.actor
	prod := in.kind == apitypes.KindProduction
	agent := isAgent(a)
	perm := apitypes.DataPermission{ConfirmCommit: prod, Why: []apitypes.DataPermissionStep{}}
	step := func(s apitypes.DataPermissionStep) {
		s.Level, s.Sensitive = perm.Level, perm.Sensitive
		perm.Why = append(perm.Why, s)
	}
	kind := kindWords(in.kind)

	if rank(a.Scope) >= rank(apitypes.ScopeAdmin) {
		perm.Level, perm.Sensitive, perm.Approve, perm.Manage = apitypes.LevelWrite, apitypes.SensitiveVisible, true, true
		step(apitypes.DataPermissionStep{Source: apitypes.PermissionAdmin, Detail: "admins can do everything"})
		if agent && prod {
			perm.Level, perm.Approve = apitypes.LevelPropose, false
			step(apitypes.DataPermissionStep{Source: apitypes.PermissionProduction, Detail: "tokens only propose changes on production; a person approves them"})
		}
		return perm
	}

	ruleStep := func(src apitypes.DataPermissionSource, r apitypes.DataPolicyRule, prefix string) {
		step(apitypes.DataPermissionStep{
			Source: src, Policy: r.From, Subject: r.Subject,
			Detail: fmt.Sprintf("%s%s: %s gets %s on %s, sensitive %s", prefix, in.policyWords(r.From), r.Subject, r.Level, kind, r.Sensitive),
		})
	}
	ownData := in.token != nil && agent && tokenData(*in.token) != nil
	switch {
	case ownData:
		d := tokenData(*in.token)
		perm.Level, perm.Sensitive = d.NonProduction, d.Sensitive
		if prod {
			perm.Level = d.Production
		}
		step(apitypes.DataPermissionStep{Source: apitypes.PermissionToken,
			Detail: fmt.Sprintf("token %q's own access: %s on %s, sensitive %s", a.Name, perm.Level, kind, perm.Sensitive)})
	default:
		subject := subjectOf(a.Scope)
		if agent {
			subject = apitypes.SubjectTokens
		}
		r := in.rules.resolve(subject, in.kind)
		perm.Level, perm.Sensitive = r.Level, r.Sensitive
		ruleStep(apitypes.PermissionPolicy, r, "")
	}
	if a.Kind != "token" {
		return perm
	}

	// A token never gets more than a person in its place would.
	capTo := func(r apitypes.DataPolicyRule, why string) {
		level, sens := perm.Level.Min(r.Level), lessSensitive(perm.Sensitive, r.Sensitive)
		if level == perm.Level && sens == perm.Sensitive {
			return
		}
		perm.Level, perm.Sensitive = level, sens
		ruleStep(apitypes.PermissionCap, r, why)
	}
	if agent {
		capTo(in.rules.resolve(subjectOf(a.Scope), in.kind), fmt.Sprintf("capped by the token's scope %s: ", a.Scope))
	}
	if in.creator != nil && rank(*in.creator) < rank(apitypes.ScopeAdmin) {
		who := "its creator " + in.token.CreatedBy
		if !agent {
			who = "its user " + in.token.CreatedBy
		}
		capTo(in.rules.resolve(subjectOf(*in.creator), in.kind), fmt.Sprintf("capped by %s's current role %s: ", who, *in.creator))
	}
	if agent && prod && perm.Level == apitypes.LevelWrite {
		perm.Level = apitypes.LevelPropose
		step(apitypes.DataPermissionStep{Source: apitypes.PermissionProduction, Detail: "tokens only propose changes on production; a person approves them"})
	}
	return perm
}

// policyWords names a policy scope in an explanation.
func (in permissionInput) policyWords(from apitypes.DataPolicyScope) string {
	switch from {
	case apitypes.PolicyDefault:
		return "default policy"
	case apitypes.PolicyProject:
		return "project " + in.project
	case apitypes.PolicyEnvironment:
		return "environment " + in.project + "/" + in.env
	}
	return "built-in default"
}

// dataPermission loads what decides the actor's data permission in an
// environment (at most three queries: the token, its creator and the
// rules) and decides it. Admins need none.
func (c *Core) dataPermission(ctx context.Context, a Actor, es envScope) (apitypes.DataPermission, error) {
	in := permissionInput{actor: a, kind: apitypes.EnvironmentKind(es.env.Kind), project: es.project.Name, env: es.env.Name}
	if rank(a.Scope) < rank(apitypes.ScopeAdmin) {
		if a.Kind == "token" {
			t, err := c.store.GetToken(ctx, a.TokenID)
			if err != nil {
				return apitypes.DataPermission{}, fmt.Errorf("load token %d: %w", a.TokenID, err)
			}
			in.token = &t
			if u, err := c.store.GetUserByEmail(ctx, t.CreatedBy); err == nil {
				role := apitypes.Scope(u.Role)
				in.creator = &role
			} else if !store.IsNotFound(err) {
				return apitypes.DataPermission{}, err
			}
		}
		rules, err := c.store.ListEffectiveDataPolicyRules(ctx, store.ListEffectiveDataPolicyRulesParams{ProjectID: es.project.ID, EnvironmentID: es.env.ID})
		if err != nil {
			return apitypes.DataPermission{}, err
		}
		in.rules = rules
	}
	return decidePermission(in), nil
}

// isAgent reports whether the actor is a token in its own right. A `dock
// login` token (AuthenticateToken sets its Email) acts for its user.
func isAgent(a Actor) bool { return a.Kind == "token" && a.Email == "" }

// requireLevel checks a permission allows level, saying why not.
func requireLevel(perm apitypes.DataPermission, need apitypes.DataLevel, action string) error {
	if perm.Level.Includes(need) {
		return nil
	}
	return errf(apitypes.CodeForbidden, "%s needs data access level %s; you have %s (%s)", action, need, perm.Level, whyWords(perm))
}

// whyWords joins a permission's explanation.
func whyWords(perm apitypes.DataPermission) string {
	parts := make([]string, len(perm.Why))
	for i, s := range perm.Why {
		parts[i] = s.Detail
	}
	return strings.Join(parts, "; ")
}

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
	return c.loadPolicy(ctx, pol, p, e)
}

// loadPolicy fills a policy's rules and effective rules (two queries).
func (c *Core) loadPolicy(ctx context.Context, pol apitypes.DataPolicy, p *store.Project, e *store.Environment) (apitypes.DataPolicy, error) {
	pid, eid := scopeIDs(p, e)
	own, err := c.store.ListDataPolicyRules(ctx, store.ListDataPolicyRulesParams{ProjectID: pid, EnvironmentID: eid})
	if err != nil {
		return pol, err
	}
	all := policyRules(own)
	if p != nil {
		if all, err = c.store.ListEffectiveDataPolicyRules(ctx, store.ListEffectiveDataPolicyRulesParams{ProjectID: pid, EnvironmentID: eid}); err != nil {
			return pol, err
		}
	}
	pol.Rules = make([]apitypes.DataPolicyRule, len(own))
	for i, r := range own {
		pol.Rules[i] = toPolicyRule(r)
	}
	kinds := envKinds
	if e != nil {
		kinds = []apitypes.EnvironmentKind{apitypes.EnvironmentKind(e.Kind)}
	}
	pol.Effective = make([]apitypes.DataPolicyRule, 0, len(dataSubjects)*len(kinds))
	for _, s := range dataSubjects {
		for _, k := range kinds {
			pol.Effective = append(pol.Effective, all.resolve(s, k))
		}
	}
	return pol, nil
}

// validLevel reports whether l is a data access level.
func validLevel(l apitypes.DataLevel) bool {
	return l == apitypes.LevelNone || l.Includes(apitypes.LevelSchema)
}

func validSensitivity(s apitypes.DataSensitivity) bool {
	return s == apitypes.SensitiveMasked || s == apitypes.SensitiveVisible
}

// SetDataPolicy replaces the rules at one scope (see GetDataPolicy).
// Admin only; the default policy needs an unlimited admin. An
// environment's rules may only name its own kind.
func (c *Core) SetDataPolicy(ctx context.Context, a Actor, ref EnvRef, req apitypes.UpdateDataPolicyRequest) (apitypes.DataPolicy, error) {
	pol, p, e, err := c.policyScope(ctx, a, ref, apitypes.ScopeAdmin, "changing the data access policy")
	if err != nil {
		return pol, err
	}
	seen := map[string]bool{}
	for _, r := range req.Rules {
		switch {
		case r.Subject != apitypes.SubjectReadUsers && r.Subject != apitypes.SubjectDeployUsers && r.Subject != apitypes.SubjectTokens:
			return pol, invalid("data policy subject %q must be role:read, role:deploy or tokens", r.Subject)
		case !validKind(r.Kind):
			return pol, invalid("environment kind %q must be production or non_production", r.Kind)
		case !validLevel(r.Level):
			return pol, invalid("data access level %q must be none, schema, read, propose or write", r.Level)
		case !validSensitivity(r.Sensitive):
			return pol, invalid("sensitive %q must be masked or visible", r.Sensitive)
		case e != nil && string(r.Kind) != e.Kind:
			return pol, invalid("environment %s/%s is %s; its data policy can only have %s rules", p.Name, e.Name, kindWords(apitypes.EnvironmentKind(e.Kind)), kindWords(apitypes.EnvironmentKind(e.Kind)))
		}
		key := string(r.Subject) + "/" + string(r.Kind)
		if seen[key] {
			return pol, invalid("two rules for %s on %s; keep one", r.Subject, kindWords(r.Kind))
		}
		seen[key] = true
	}
	pid, eid := scopeIDs(p, e)
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteDataPolicyRules(ctx, store.DeleteDataPolicyRulesParams{ProjectID: pid, EnvironmentID: eid}); err != nil {
			return err
		}
		for _, r := range req.Rules {
			params := store.AddDataPolicyRuleParams{Subject: string(r.Subject), Kind: string(r.Kind), Level: string(r.Level), Sensitive: string(r.Sensitive)}
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
	summary := "cleared the data access policy of " + target + "; it inherits everything"
	if len(req.Rules) > 0 {
		parts := make([]string, len(req.Rules))
		for i, r := range req.Rules {
			parts[i] = fmt.Sprintf("%s %s on %s (sensitive %s)", r.Subject, r.Level, kindWords(r.Kind), r.Sensitive)
		}
		summary = "set the data access policy of " + target + ": " + strings.Join(parts, ", ")
	}
	c.audit(ctx, a, apitypes.AuditDataPolicy, p, e, target, summary)
	return c.loadPolicy(ctx, pol, p, e)
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
	if !validLevel(d.Production) || !validLevel(d.NonProduction) {
		return params, invalid("token data levels must be none, schema, read, propose or write")
	}
	if !validSensitivity(d.Sensitive) {
		return params, invalid("token data sensitive must be masked or visible")
	}
	prod, nonProd, sens := string(d.Production), string(d.NonProduction), string(d.Sensitive)
	params.DataProduction, params.DataNonProduction, params.DataSensitive = &prod, &nonProd, &sens
	return params, nil
}

// describeTokenData says what a token data override is, for the audit log.
func describeTokenData(d *apitypes.DataTokenAccess) string {
	if d == nil {
		return "follows the data policy's tokens rule"
	}
	return fmt.Sprintf("data access %s on production, %s on non-production, sensitive %s (capped by its scope and creator)", d.Production, d.NonProduction, d.Sensitive)
}

// UpdateToken changes a token's data access override; null follows the
// policy again. Admin only (unlimited). The override is stored as given
// and capped when it is used, by what users of the token's scope and its
// creator's current role get then.
func (c *Core) UpdateToken(ctx context.Context, a Actor, id int64, req apitypes.UpdateTokenRequest) (apitypes.Token, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "managing tokens"); err != nil {
		return apitypes.Token{}, err
	}
	t, err := c.store.GetToken(ctx, id)
	if store.IsNotFound(err) {
		return apitypes.Token{}, notFound("token %d not found", id)
	}
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
	c.audit(ctx, a, apitypes.AuditDataPolicy, nil, nil, "token "+t.Name, fmt.Sprintf("token %q (id %d) %s", t.Name, t.ID, describeTokenData(req.Data)))
	return toToken(ctx, newNames(c.store), t), nil
}
