package datapanel

import "github.com/NordTempo/tempoDock/internal/apitypes"

// The data access policy's inputs (ADR 0016 section 4, ADR 0024 section
// 2): a host loads them (Dock: the actor's scope, token and creator, and
// the default, project and environment rules; tempoBase: the user's role,
// the token's creator, and the default and connection rules) and Decide
// turns them into an apitypes.DataPermission with the steps that decided
// it. Decide moves here from core's decidePermission in workstream A:
//
//	func Decide(in PolicyInput) apitypes.DataPermission

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
