package tempobase

import (
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
)

// Pending ADR 0024 A.1: the policy functions below have the signatures
// the ADR gives datapanel's (Decide, ValidateRules, Effective,
// DescribeRules, ValidateTokenAccess, DescribeTokenAccess) and follow
// Dock's core rules. When A.1 lands this file is deleted and the callers
// switch to datapanel's, so both products decide with one implementation.

// policyConnection is apitypes.PolicyConnection (ADR 0024 A.6).
const policyConnection apitypes.DataPolicyScope = "connection"

// dataSubjects and envKinds list every policy subject and environment
// kind, in the order the policy shows them.
var (
	dataSubjects = []apitypes.DataSubject{apitypes.SubjectReadUsers, apitypes.SubjectDeployUsers, apitypes.SubjectTokens}
	envKinds     = []apitypes.EnvironmentKind{apitypes.KindProduction, apitypes.KindNonProduction}
)

// scopeRank orders where a rule was set by specificity.
func scopeRank(from apitypes.DataPolicyScope) int {
	switch from {
	case apitypes.PolicyEnvironment, policyConnection:
		return 3
	case apitypes.PolicyProject:
		return 2
	case apitypes.PolicyDefault:
		return 1
	}
	return 0
}

// resolveRule is the rule for subject on kind: the most specific set,
// else the built-in one.
func resolveRule(rules []apitypes.DataPolicyRule, builtIn datapanel.BuiltIn, subject apitypes.DataSubject, kind apitypes.EnvironmentKind) apitypes.DataPolicyRule {
	best, bestRank := builtIn(subject, kind), 0
	for _, r := range rules {
		if r.Subject == subject && r.Kind == kind && scopeRank(r.From) > bestRank {
			best, bestRank = r, scopeRank(r.From)
		}
	}
	return best
}

// lessSensitive returns the stricter of two visibilities.
func lessSensitive(a, b apitypes.DataSensitivity) apitypes.DataSensitivity {
	if a == apitypes.SensitiveVisible && b == apitypes.SensitiveVisible {
		return apitypes.SensitiveVisible
	}
	return apitypes.SensitiveMasked
}

// kindWords is how messages name a kind.
func kindWords(k apitypes.EnvironmentKind) string { return strings.ReplaceAll(string(k), "_", "-") }

// decide works out what the caller may do and why, step by step (ADR
// 0016 section 4).
func decide(in datapanel.PolicyInput) apitypes.DataPermission {
	prod := in.Kind == apitypes.KindProduction
	perm := apitypes.DataPermission{ConfirmCommit: prod, Why: []apitypes.DataPermissionStep{}}
	step := func(s apitypes.DataPermissionStep) {
		s.Level, s.Sensitive = perm.Level, perm.Sensitive
		perm.Why = append(perm.Why, s)
	}
	kind := kindWords(in.Kind)
	toProposal := func() {
		step(apitypes.DataPermissionStep{Source: apitypes.PermissionProduction, Detail: "tokens only propose changes on production; a person approves them"})
	}
	if in.Admin {
		perm.Level, perm.Sensitive, perm.Approve, perm.Manage = apitypes.LevelWrite, apitypes.SensitiveVisible, true, true
		step(apitypes.DataPermissionStep{Source: apitypes.PermissionAdmin, Detail: "admins can do everything"})
		if in.Agent && prod {
			perm.Level, perm.Approve = apitypes.LevelPropose, false
			toProposal()
		}
		return perm
	}
	scopeWords := func(from apitypes.DataPolicyScope) string {
		switch from {
		case apitypes.PolicyBuiltIn, "":
			return "built-in default"
		case apitypes.PolicyDefault:
			return "default policy"
		}
		if in.ScopeName != nil {
			return in.ScopeName(from)
		}
		return string(from)
	}
	ruleStep := func(src apitypes.DataPermissionSource, r apitypes.DataPolicyRule, prefix string) {
		step(apitypes.DataPermissionStep{
			Source: src, Policy: r.From, Subject: r.Subject,
			Detail: fmt.Sprintf("%s%s: %s gets %s on %s, sensitive %s", prefix, scopeWords(r.From), r.Subject, r.Level, kind, r.Sensitive),
		})
	}
	resolve := func(s apitypes.DataSubject) apitypes.DataPolicyRule {
		return resolveRule(in.Rules, in.BuiltIn, s, in.Kind)
	}
	if in.Agent && in.Token != nil && in.Token.Override != nil {
		d := in.Token.Override
		perm.Level, perm.Sensitive = d.NonProduction, d.Sensitive
		if prod {
			perm.Level = d.Production
		}
		step(apitypes.DataPermissionStep{Source: apitypes.PermissionToken,
			Detail: fmt.Sprintf("token %q's own access: %s on %s, sensitive %s", in.Token.Name, perm.Level, kind, perm.Sensitive)})
	} else {
		subject := in.Subject
		if in.Agent {
			subject = apitypes.SubjectTokens
		}
		r := resolve(subject)
		perm.Level, perm.Sensitive = r.Level, r.Sensitive
		ruleStep(apitypes.PermissionPolicy, r, "")
	}
	if in.Token == nil {
		return perm
	}
	// A token never gets more than a person in its place would.
	for _, c := range in.Token.Caps {
		r := resolve(c.Subject)
		level, sens := perm.Level.Min(r.Level), lessSensitive(perm.Sensitive, r.Sensitive)
		if level == perm.Level && sens == perm.Sensitive {
			continue
		}
		perm.Level, perm.Sensitive = level, sens
		ruleStep(apitypes.PermissionCap, r, c.Why)
	}
	if in.Agent && prod && perm.Level == apitypes.LevelWrite {
		perm.Level = apitypes.LevelPropose
		toProposal()
	}
	return perm
}

// validLevel reports whether l is a data access level.
func validLevel(l apitypes.DataLevel) bool {
	return l == apitypes.LevelNone || l.Includes(apitypes.LevelSchema)
}

func validSensitivity(s apitypes.DataSensitivity) bool {
	return s == apitypes.SensitiveMasked || s == apitypes.SensitiveVisible
}

// validateRules checks rules for one scope: known values, one rule per
// subject and kind, and (only set) only that kind. scope names it.
func validateRules(rules []apitypes.DataPolicyRule, only apitypes.EnvironmentKind, scope string) error {
	seen := map[string]bool{}
	for _, r := range rules {
		switch {
		case r.Subject != apitypes.SubjectReadUsers && r.Subject != apitypes.SubjectDeployUsers && r.Subject != apitypes.SubjectTokens:
			return invalid("data policy subject %q must be role:read, role:deploy or tokens", r.Subject)
		case r.Kind != apitypes.KindProduction && r.Kind != apitypes.KindNonProduction:
			return invalid("kind %q must be production or non_production", r.Kind)
		case !validLevel(r.Level):
			return invalid("data access level %q must be none, schema, read, propose or write", r.Level)
		case !validSensitivity(r.Sensitive):
			return invalid("sensitive %q must be masked or visible", r.Sensitive)
		case only != "" && r.Kind != only:
			return invalid("%s is %s; its data policy can only have %s rules", scope, kindWords(only), kindWords(only))
		}
		key := string(r.Subject) + "/" + string(r.Kind)
		if seen[key] {
			return invalid("two rules for %s on %s; keep one", r.Subject, kindWords(r.Kind))
		}
		seen[key] = true
	}
	return nil
}

// effectiveRules is the rule that applies per subject and kind.
func effectiveRules(rules []apitypes.DataPolicyRule, kinds []apitypes.EnvironmentKind, builtIn datapanel.BuiltIn) []apitypes.DataPolicyRule {
	out := make([]apitypes.DataPolicyRule, 0, len(dataSubjects)*len(kinds))
	for _, s := range dataSubjects {
		for _, k := range kinds {
			out = append(out, resolveRule(rules, builtIn, s, k))
		}
	}
	return out
}

// describeRules says what a policy change set, for the audit log.
func describeRules(target string, rules []apitypes.DataPolicyRule) string {
	if len(rules) == 0 {
		return "cleared the data access policy of " + target + "; it inherits everything"
	}
	parts := make([]string, len(rules))
	for i, r := range rules {
		parts[i] = fmt.Sprintf("%s %s on %s (sensitive %s)", r.Subject, r.Level, kindWords(r.Kind), r.Sensitive)
	}
	return "set the data access policy of " + target + ": " + strings.Join(parts, ", ")
}

// validateTokenAccess checks a token's own data access; nil is valid.
func validateTokenAccess(d *apitypes.DataTokenAccess) error {
	if d == nil {
		return nil
	}
	if !validLevel(d.Production) || !validLevel(d.NonProduction) {
		return invalid("token data levels must be none, schema, read, propose or write")
	}
	if !validSensitivity(d.Sensitive) {
		return invalid("token data sensitive must be masked or visible")
	}
	return nil
}

// describeTokenAccess says what a token's own data access is, for the
// audit log.
func describeTokenAccess(d *apitypes.DataTokenAccess) string {
	if d == nil {
		return "follows the data policy's tokens rule"
	}
	return fmt.Sprintf("data access %s on production, %s on non-production, sensitive %s (capped by its owner's role)", d.Production, d.NonProduction, d.Sensitive)
}
