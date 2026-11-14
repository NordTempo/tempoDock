package core

import (
	"context"
	"fmt"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/store"
)

// The platform's part of the data access policy (ADR 0062): operators set
// a default (where an org set nothing, in place of dockBuiltIn) and a
// ceiling (the most an org's rule or token gives) per subject and
// environment kind. Orgs decide everything else in their own Data access
// settings. Org admins keep full access. The rules are a few rows, read
// on start and after each change and kept in memory, so deciding a
// permission costs no query for them.

// ruleKey is a subject in an environment kind.
type ruleKey struct {
	subject apitypes.DataSubject
	kind    apitypes.EnvironmentKind
}

// platformPolicy is the platform's rules as Core keeps them; nil maps
// mean none set.
type platformPolicy struct {
	defaults map[ruleKey]apitypes.DataPolicyRule
	ceilings map[ruleKey]apitypes.DataPolicyRule
}

// builtIn is the rule where an org set nothing: the platform's default,
// else Dock's built-in one.
func (p *platformPolicy) builtIn(subject apitypes.DataSubject, kind apitypes.EnvironmentKind) apitypes.DataPolicyRule {
	if p != nil {
		if r, ok := p.defaults[ruleKey{subject, kind}]; ok {
			r.From = apitypes.PolicyPlatform
			return r
		}
	}
	return dockBuiltIn(subject, kind)
}

// ceiling is the most a subject gets in an environment kind; ok is false
// without one.
func (p *platformPolicy) ceiling(subject apitypes.DataSubject, kind apitypes.EnvironmentKind) (apitypes.DataPolicyRule, bool) {
	if p == nil {
		return apitypes.DataPolicyRule{}, false
	}
	r, ok := p.ceilings[ruleKey{subject, kind}]
	return r, ok
}

// capPermission caps a decided permission at the platform's ceiling for
// the subject, saying so in Why. Admins aren't capped.
func (p *platformPolicy) capPermission(perm apitypes.DataPermission, admin bool, subject apitypes.DataSubject, kind apitypes.EnvironmentKind) apitypes.DataPermission {
	c, ok := p.ceiling(subject, kind)
	if !ok || admin {
		return perm
	}
	level, sens := perm.Level.Min(c.Level), perm.Sensitive
	if c.Sensitive == apitypes.SensitiveMasked {
		sens = apitypes.SensitiveMasked
	}
	if level == perm.Level && sens == perm.Sensitive {
		return perm
	}
	perm.Level, perm.Sensitive = level, sens
	if !level.Includes(apitypes.LevelWrite) {
		perm.Approve, perm.Manage = false, false
	}
	perm.Why = append(perm.Why, apitypes.DataPermissionStep{
		Source: apitypes.PermissionCap, Level: level, Sensitive: sens, Subject: subject,
		Detail: fmt.Sprintf("capped by the platform's ceiling for %s in %s: %s, sensitive columns %s", subject, kind, c.Level, c.Sensitive),
	})
	return perm
}

// loadPlatformPolicy reads the platform's rules into memory.
func (c *Core) loadPlatformPolicy(ctx context.Context) error {
	rows, err := c.store.ListPlatformDataPolicy(ctx)
	if err != nil {
		return fmt.Errorf("load the platform's data policy: %w", err)
	}
	p := &platformPolicy{defaults: map[ruleKey]apitypes.DataPolicyRule{}, ceilings: map[ruleKey]apitypes.DataPolicyRule{}}
	for _, r := range rows {
		rule := apitypes.DataPolicyRule{Subject: apitypes.DataSubject(r.Subject), Kind: apitypes.EnvironmentKind(r.Kind), Level: apitypes.DataLevel(r.Level), Sensitive: apitypes.DataSensitivity(r.Sensitive)}
		k := ruleKey{rule.Subject, rule.Kind}
		if r.Rule == "ceiling" {
			p.ceilings[k] = rule
		} else {
			p.defaults[k] = rule
		}
	}
	c.platformPolicy.Store(p)
	return nil
}

// PlatformDataPolicy returns the platform's default and ceiling.
// Operators only.
func (c *Core) PlatformDataPolicy(ctx context.Context, a Actor) (apitypes.PlatformDataPolicy, error) {
	if err := a.requirePlatform(PlatformView, "reading the platform's data policy"); err != nil {
		return apitypes.PlatformDataPolicy{}, err
	}
	return c.platformPolicyView(), nil
}

// platformPolicyView is the platform's rules for the API.
func (c *Core) platformPolicyView() apitypes.PlatformDataPolicy {
	p := c.platformPolicy.Load()
	out := apitypes.PlatformDataPolicy{Defaults: []apitypes.DataPolicyRule{}, Ceilings: []apitypes.DataPolicyRule{}}
	for _, k := range allRuleKeys() {
		if r, ok := p.defaults[k]; ok {
			out.Defaults = append(out.Defaults, r)
		}
		if r, ok := p.ceilings[k]; ok {
			out.Ceilings = append(out.Ceilings, r)
		}
		out.BuiltIn = append(out.BuiltIn, dockBuiltIn(k.subject, k.kind))
	}
	return out
}

// allRuleKeys is every subject in every kind, in a stable order.
func allRuleKeys() []ruleKey {
	var out []ruleKey
	for _, s := range []apitypes.DataSubject{apitypes.SubjectReadUsers, apitypes.SubjectDeployUsers, apitypes.SubjectTokens} {
		for _, k := range datapanel.Kinds {
			out = append(out, ruleKey{s, k})
		}
	}
	return out
}

// SetPlatformDataPolicy replaces the platform's default and ceiling. A
// default above its ceiling is refused. Org rules already above a new
// ceiling stay, and are capped when they apply. Operators only.
func (c *Core) SetPlatformDataPolicy(ctx context.Context, a Actor, req apitypes.PlatformDataPolicy) (apitypes.PlatformDataPolicy, error) {
	if err := a.requirePlatform(PlatformSettings, "changing the platform's data policy"); err != nil {
		return apitypes.PlatformDataPolicy{}, err
	}
	if err := datapanel.ValidateRules(req.Defaults, "", "the platform's default"); err != nil {
		return apitypes.PlatformDataPolicy{}, err
	}
	if err := datapanel.ValidateRules(req.Ceilings, "", "the platform's ceiling"); err != nil {
		return apitypes.PlatformDataPolicy{}, err
	}
	ceil := map[ruleKey]apitypes.DataPolicyRule{}
	for _, r := range req.Ceilings {
		ceil[ruleKey{r.Subject, r.Kind}] = r
	}
	for _, r := range req.Defaults {
		if cr, ok := ceil[ruleKey{r.Subject, r.Kind}]; ok && aboveCeiling(r, cr) {
			return apitypes.PlatformDataPolicy{}, invalid("the default for %s in %s (%s) is above its ceiling (%s); lower one of them", r.Subject, r.Kind, r.Level, cr.Level)
		}
	}
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeletePlatformDataPolicy(ctx); err != nil {
			return err
		}
		for rule, rs := range map[string][]apitypes.DataPolicyRule{"default": req.Defaults, "ceiling": req.Ceilings} {
			for _, r := range rs {
				if err := q.AddPlatformDataPolicyRule(ctx, store.AddPlatformDataPolicyRuleParams{
					Rule: rule, Subject: string(r.Subject), Kind: string(r.Kind), Level: string(r.Level), Sensitive: string(r.Sensitive),
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return apitypes.PlatformDataPolicy{}, err
	}
	if err := c.loadPlatformPolicy(ctx); err != nil {
		return apitypes.PlatformDataPolicy{}, err
	}
	c.audit(ctx, a.platform(), apitypes.AuditDataPolicy, nil, nil, "platform data policy",
		datapanel.DescribeRules("the platform's default", req.Defaults)+"; "+datapanel.DescribeRules("the platform's ceiling", req.Ceilings))
	return c.platformPolicyView(), nil
}

// aboveCeiling reports whether rule gives more than ceiling.
func aboveCeiling(rule, ceiling apitypes.DataPolicyRule) bool {
	return !ceiling.Level.Includes(rule.Level) || (rule.Sensitive == apitypes.SensitiveVisible && ceiling.Sensitive == apitypes.SensitiveMasked)
}

// checkCeiling refuses org rules above the platform's ceiling.
func (c *Core) checkCeiling(rules []apitypes.DataPolicyRule) error {
	p := c.platformPolicy.Load()
	for _, r := range rules {
		if cr, ok := p.ceiling(r.Subject, r.Kind); ok && aboveCeiling(r, cr) {
			return invalid("the platform allows at most %s (sensitive columns %s) for %s in %s; ask a platform operator to raise its ceiling", cr.Level, cr.Sensitive, r.Subject, r.Kind)
		}
	}
	return nil
}
