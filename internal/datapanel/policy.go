package datapanel

import (
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The data access policy (ADR 0016 section 4, ADR 0024 section 2): admins
// set, per subject and environment kind, a level and sensitive-column
// visibility at a host's scopes (Dock: default, project, environment;
// tempoBase: default, connection). Per subject and kind the most specific
// rule wins, then the host's built-in default. A host loads a caller's
// PolicyInput and Decide turns it into an apitypes.DataPermission with the
// steps that decided it.

// Subjects and Kinds list every policy subject and environment kind, in
// the order a policy shows them.
var (
	Subjects = []apitypes.DataSubject{apitypes.SubjectReadUsers, apitypes.SubjectDeployUsers, apitypes.SubjectTokens}
	Kinds    = []apitypes.EnvironmentKind{apitypes.KindProduction, apitypes.KindNonProduction}
)

// BuiltIn is a host's built-in rule for a subject and environment kind:
// what applies when no rule is set at any scope. Its From is
// apitypes.PolicyBuiltIn.
type BuiltIn func(apitypes.DataSubject, apitypes.EnvironmentKind) apitypes.DataPolicyRule

// PolicyInput is everything that decides one caller's permission on one
// database.
type PolicyInput struct {
	// Kind is the database's environment kind: Dock's environment kind,
	// tempoBase's per-connection production flag.
	Kind apitypes.EnvironmentKind
	// Admin callers can do everything (write, sensitive visible, approve,
	// manage). An admin agent still only proposes on production.
	Admin bool
	// Agent is a token acting in its own right: it follows the tokens
	// rule or its own override, and only proposes on production. Dock's
	// `dock login` tokens act for their user and aren't agents.
	Agent bool
	// Subject is the rule a non-admin person (or a token acting for one)
	// follows: role:read or role:deploy. Agents follow tokens unless they
	// have an override.
	Subject apitypes.DataSubject
	// Token is set for every token caller, agent or not.
	Token *TokenInput
	// Rules are the rules set at every scope that applies here, with From
	// saying which scope; per subject and kind the most specific wins
	// (environment or connection, then project, then default), else
	// BuiltIn.
	Rules   []apitypes.DataPolicyRule
	BuiltIn BuiltIn
	// ScopeName names a scope in explanations: "project shop",
	// "connection orders". Built-in and default are named by Decide.
	ScopeName func(apitypes.DataPolicyScope) string
}

// TokenInput is what the policy needs about a token.
type TokenInput struct {
	Name string
	// Override is the token's own data access; nil follows the rules.
	Override *apitypes.DataTokenAccess
	// Caps are rules that bound the token, applied in order: a token
	// never gets more than a person in its place would. Dock: the token's
	// scope's subject (agents), then its creator's current role unless
	// the creator is an admin. tempoBase: the creator's current role
	// unless admin.
	Caps []Cap
}

// Cap bounds a token by the rule of Subject; Why prefixes the step's
// explanation ("capped by the token's scope deploy: ").
type Cap struct {
	Subject apitypes.DataSubject
	Why     string
}

// productionWhy explains why a token only proposes on production.
const productionWhy = "tokens only propose changes on production; a person approves them"

// Decide works out what the caller may do and why, step by step. Admins
// get everything (an admin agent only proposes on production). Others get
// the rule for their subject, or an agent's own override, then a token is
// capped by in.Token.Caps, and an agent's write on production becomes
// propose. The last step always carries the final level and visibility.
func Decide(in PolicyInput) apitypes.DataPermission {
	prod := in.Kind == apitypes.KindProduction
	perm := apitypes.DataPermission{ConfirmCommit: prod, Why: []apitypes.DataPermissionStep{}}
	step := func(s apitypes.DataPermissionStep) {
		s.Level, s.Sensitive = perm.Level, perm.Sensitive
		perm.Why = append(perm.Why, s)
	}
	kind := KindWords(in.Kind)

	if in.Admin {
		perm.Level, perm.Sensitive, perm.Approve, perm.Manage = apitypes.LevelWrite, apitypes.SensitiveVisible, true, true
		step(apitypes.DataPermissionStep{Source: apitypes.PermissionAdmin, Detail: "admins can do everything"})
		if in.Agent && prod {
			perm.Level, perm.Approve = apitypes.LevelPropose, false
			step(apitypes.DataPermissionStep{Source: apitypes.PermissionProduction, Detail: productionWhy})
		}
		return perm
	}

	ruleStep := func(src apitypes.DataPermissionSource, r apitypes.DataPolicyRule, prefix string) {
		step(apitypes.DataPermissionStep{
			Source: src, Policy: r.From, Subject: r.Subject,
			Detail: fmt.Sprintf("%s%s: %s gets %s on %s, sensitive %s", prefix, in.scopeWords(r.From), r.Subject, r.Level, kind, r.Sensitive),
		})
	}
	if d := in.override(); d != nil {
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
		r := in.resolve(subject)
		perm.Level, perm.Sensitive = r.Level, r.Sensitive
		ruleStep(apitypes.PermissionPolicy, r, "")
	}
	if in.Token == nil {
		return perm
	}

	// A token never gets more than a person in its place would.
	for _, c := range in.Token.Caps {
		r := in.resolve(c.Subject)
		level, sens := perm.Level.Min(r.Level), lessSensitive(perm.Sensitive, r.Sensitive)
		if level == perm.Level && sens == perm.Sensitive {
			continue
		}
		perm.Level, perm.Sensitive = level, sens
		ruleStep(apitypes.PermissionCap, r, c.Why)
	}
	if in.Agent && prod && perm.Level == apitypes.LevelWrite {
		perm.Level = apitypes.LevelPropose
		step(apitypes.DataPermissionStep{Source: apitypes.PermissionProduction, Detail: productionWhy})
	}
	return perm
}

// override is an agent's own data access; nil when it follows the rules.
func (in PolicyInput) override() *apitypes.DataTokenAccess {
	if in.Token == nil || !in.Agent {
		return nil
	}
	return in.Token.Override
}

// resolve returns the rule that applies to subject for in.Kind.
func (in PolicyInput) resolve(subject apitypes.DataSubject) apitypes.DataPolicyRule {
	return Resolve(in.Rules, subject, in.Kind, in.BuiltIn)
}

// scopeWords names a policy scope in an explanation.
func (in PolicyInput) scopeWords(from apitypes.DataPolicyScope) string {
	switch from {
	case apitypes.PolicyDefault:
		return "default policy"
	case apitypes.PolicyBuiltIn, "":
		return "built-in default"
	}
	if in.ScopeName != nil {
		if s := in.ScopeName(from); s != "" {
			return s
		}
	}
	return string(from)
}

// scopeRank orders scopes by specificity: the most specific rule wins.
func scopeRank(from apitypes.DataPolicyScope) int {
	switch from {
	case apitypes.PolicyEnvironment, apitypes.PolicyConnection:
		return 3
	case apitypes.PolicyProject:
		return 2
	case apitypes.PolicyDefault:
		return 1
	}
	return 0
}

// Resolve returns the rule that applies to subject in databases of kind:
// the most specific one in rules, else builtIn's (none and masked without
// one). From says which.
func Resolve(rules []apitypes.DataPolicyRule, subject apitypes.DataSubject, kind apitypes.EnvironmentKind, builtIn BuiltIn) apitypes.DataPolicyRule {
	var best apitypes.DataPolicyRule
	if builtIn != nil {
		best = builtIn(subject, kind)
	} else {
		best = apitypes.DataPolicyRule{Subject: subject, Kind: kind, Level: apitypes.LevelNone, Sensitive: apitypes.SensitiveMasked}
	}
	best.From = apitypes.PolicyBuiltIn
	bestRank := 0
	for _, r := range rules {
		if r.Subject != subject || r.Kind != kind {
			continue
		}
		if rank := scopeRank(r.From); rank > bestRank {
			best, bestRank = r, rank
		}
	}
	return best
}

// Effective returns the rule that applies per subject (Subjects) and kind
// (kinds), in that order: a policy's effective rules.
func Effective(rules []apitypes.DataPolicyRule, kinds []apitypes.EnvironmentKind, builtIn BuiltIn) []apitypes.DataPolicyRule {
	out := make([]apitypes.DataPolicyRule, 0, len(Subjects)*len(kinds))
	for _, s := range Subjects {
		for _, k := range kinds {
			out = append(out, Resolve(rules, s, k, builtIn))
		}
	}
	return out
}

// lessSensitive returns the stricter of two visibilities.
func lessSensitive(a, b apitypes.DataSensitivity) apitypes.DataSensitivity {
	if a == apitypes.SensitiveVisible && b == apitypes.SensitiveVisible {
		return apitypes.SensitiveVisible
	}
	return apitypes.SensitiveMasked
}

// RequireLevel checks a permission allows level, saying why not.
func RequireLevel(perm apitypes.DataPermission, need apitypes.DataLevel, action string) error {
	if perm.Level.Includes(need) {
		return nil
	}
	return Errorf(apitypes.CodeForbidden, "%s needs data access level %s; you have %s (%s)", action, need, perm.Level, Why(perm))
}

// Why joins a permission's explanation into one line.
func Why(perm apitypes.DataPermission) string {
	parts := make([]string, len(perm.Why))
	for i, s := range perm.Why {
		parts[i] = s.Detail
	}
	return strings.Join(parts, "; ")
}

// KindWords is how messages name an environment kind: "non-production".
func KindWords(k apitypes.EnvironmentKind) string {
	return strings.ReplaceAll(string(k), "_", "-")
}

// validKind reports whether k is an environment kind.
func validKind(k apitypes.EnvironmentKind) bool {
	return k == apitypes.KindProduction || k == apitypes.KindNonProduction
}

// validLevel reports whether l is a data access level.
func validLevel(l apitypes.DataLevel) bool {
	return l == apitypes.LevelNone || l.Includes(apitypes.LevelSchema)
}

func validSensitivity(s apitypes.DataSensitivity) bool {
	return s == apitypes.SensitiveMasked || s == apitypes.SensitiveVisible
}

// ValidateRules checks rules for one scope: known subjects, kinds, levels
// and visibilities, at most one rule per subject and kind, and, when only
// is set, rules for that kind alone (an environment's or connection's own
// kind; scope names it in the message, "environment shop/staging").
func ValidateRules(rules []apitypes.DataPolicyRule, only apitypes.EnvironmentKind, scope string) error {
	seen := map[string]bool{}
	for _, r := range rules {
		switch {
		case r.Subject != apitypes.SubjectReadUsers && r.Subject != apitypes.SubjectDeployUsers && r.Subject != apitypes.SubjectTokens:
			return Errorf(apitypes.CodeValidation, "data policy subject %q must be role:read, role:deploy or tokens", r.Subject)
		case !validKind(r.Kind):
			return Errorf(apitypes.CodeValidation, "environment kind %q must be production or non_production", r.Kind)
		case !validLevel(r.Level):
			return Errorf(apitypes.CodeValidation, "data access level %q must be none, schema, read, propose or write", r.Level)
		case !validSensitivity(r.Sensitive):
			return Errorf(apitypes.CodeValidation, "sensitive %q must be masked or visible", r.Sensitive)
		case only != "" && r.Kind != only:
			return Errorf(apitypes.CodeValidation, "%s is %s; its data policy can only have %s rules", scope, KindWords(only), KindWords(only))
		}
		key := string(r.Subject) + "/" + string(r.Kind)
		if seen[key] {
			return Errorf(apitypes.CodeValidation, "two rules for %s on %s; keep one", r.Subject, KindWords(r.Kind))
		}
		seen[key] = true
	}
	return nil
}

// DescribeRules is the audit summary of setting target's rules (target is
// "default data policy", "shop/staging", ...).
func DescribeRules(target string, rules []apitypes.DataPolicyRule) string {
	if len(rules) == 0 {
		return "cleared the data access policy of " + target + "; it inherits everything"
	}
	parts := make([]string, len(rules))
	for i, r := range rules {
		parts[i] = fmt.Sprintf("%s %s on %s (sensitive %s)", r.Subject, r.Level, KindWords(r.Kind), r.Sensitive)
	}
	return "set the data access policy of " + target + ": " + strings.Join(parts, ", ")
}

// ValidateTokenAccess checks a token's data override; nil (follow the
// policy) is valid.
func ValidateTokenAccess(d *apitypes.DataTokenAccess) error {
	switch {
	case d == nil:
		return nil
	case !validLevel(d.Production) || !validLevel(d.NonProduction):
		return Errorf(apitypes.CodeValidation, "token data levels must be none, schema, read, propose or write")
	case !validSensitivity(d.Sensitive):
		return Errorf(apitypes.CodeValidation, "token data sensitive must be masked or visible")
	}
	return nil
}

// DescribeTokenAccess says what a token data override is, for the audit
// log.
func DescribeTokenAccess(d *apitypes.DataTokenAccess) string {
	if d == nil {
		return "follows the data policy's tokens rule"
	}
	return fmt.Sprintf("data access %s on production, %s on non-production, sensitive %s (capped by its scope and creator)", d.Production, d.NonProduction, d.Sensitive)
}
