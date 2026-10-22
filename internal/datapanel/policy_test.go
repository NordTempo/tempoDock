package datapanel

import (
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

const (
	prod    = apitypes.KindProduction
	nonProd = apitypes.KindNonProduction
	masked  = apitypes.SensitiveMasked
	visible = apitypes.SensitiveVisible
)

// tbBuiltIn is a tempoBase-like host's built-in rules: viewers read,
// editors write, tokens get nothing.
func tbBuiltIn(s apitypes.DataSubject, k apitypes.EnvironmentKind) apitypes.DataPolicyRule {
	level := apitypes.LevelNone
	switch s {
	case apitypes.SubjectReadUsers:
		level = apitypes.LevelRead
	case apitypes.SubjectDeployUsers:
		level = apitypes.LevelWrite
	}
	return apitypes.DataPolicyRule{Subject: s, Kind: k, Level: level, Sensitive: masked}
}

func rule(from apitypes.DataPolicyScope, s apitypes.DataSubject, k apitypes.EnvironmentKind, l apitypes.DataLevel, sens apitypes.DataSensitivity) apitypes.DataPolicyRule {
	return apitypes.DataPolicyRule{Subject: s, Kind: k, Level: l, Sensitive: sens, From: from}
}

func sources(p apitypes.DataPermission) string {
	var out []string
	for _, s := range p.Why {
		out = append(out, string(s.Source))
	}
	return strings.Join(out, ",")
}

// TestDecide covers the decision on a host with connection scopes and
// its own built-in rules; Dock's mapping is tested in core.
func TestDecide(t *testing.T) {
	scope := func(s apitypes.DataPolicyScope) string { return "connection orders" }
	rules := []apitypes.DataPolicyRule{
		rule(apitypes.PolicyDefault, apitypes.SubjectTokens, prod, apitypes.LevelWrite, visible),
		rule(apitypes.PolicyConnection, apitypes.SubjectTokens, prod, apitypes.LevelRead, visible),
		rule(apitypes.PolicyDefault, apitypes.SubjectDeployUsers, nonProd, apitypes.LevelPropose, masked),
	}
	viewerCap := []Cap{{Subject: apitypes.SubjectReadUsers, Why: "capped by its creator's current role viewer: "}}
	for _, tc := range []struct {
		name    string
		in      PolicyInput
		level   apitypes.DataLevel
		sens    apitypes.DataSensitivity
		sources string
		detail  string
	}{
		{"admin", PolicyInput{Kind: prod, Admin: true}, apitypes.LevelWrite, visible, "admin", "admins can do everything"},
		{"built-in editor", PolicyInput{Kind: prod, Subject: apitypes.SubjectDeployUsers}, apitypes.LevelWrite, masked, "policy",
			"built-in default: role:deploy gets write on production, sensitive masked"},
		{"default beats built-in", PolicyInput{Kind: nonProd, Subject: apitypes.SubjectDeployUsers, Rules: rules}, apitypes.LevelPropose, masked, "policy",
			"default policy: role:deploy gets propose on non-production, sensitive masked"},
		{"connection beats default", PolicyInput{Kind: prod, Agent: true, Token: &TokenInput{Name: "bot"}, Rules: rules}, apitypes.LevelRead, visible, "policy",
			"connection orders: tokens gets read on production, sensitive visible"},
		{"creator caps the token", PolicyInput{Kind: prod, Agent: true, Token: &TokenInput{Name: "bot", Caps: viewerCap}, Rules: rules}, apitypes.LevelRead, masked, "policy,cap",
			"capped by its creator's current role viewer: built-in default: role:read gets read on production, sensitive masked"},
		{"override write on production proposes", PolicyInput{Kind: prod, Agent: true, Token: &TokenInput{Name: "bot", Override: &apitypes.DataTokenAccess{
			Production: apitypes.LevelWrite, NonProduction: apitypes.LevelNone, Sensitive: masked}}}, apitypes.LevelPropose, masked, "token,production",
			"tokens only propose changes on production; a person approves them"},
		{"an override only counts for agents", PolicyInput{Kind: nonProd, Subject: apitypes.SubjectReadUsers, Token: &TokenInput{Name: "cli", Override: &apitypes.DataTokenAccess{
			Production: apitypes.LevelWrite, NonProduction: apitypes.LevelWrite, Sensitive: visible}}}, apitypes.LevelRead, masked, "policy", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.BuiltIn, tc.in.ScopeName = tbBuiltIn, scope
			got := Decide(tc.in)
			last := got.Why[len(got.Why)-1]
			if got.Level != tc.level || got.Sensitive != tc.sens || sources(got) != tc.sources || last.Level != got.Level || last.Sensitive != got.Sensitive ||
				(tc.detail != "" && last.Detail != tc.detail) || got.ConfirmCommit != (tc.in.Kind == prod) || got.Manage != tc.in.Admin {
				t.Fatalf("got %s/%s %s %+v", got.Level, got.Sensitive, sources(got), got.Why)
			}
		})
	}
}

func TestValidateRules(t *testing.T) {
	r := func(s apitypes.DataSubject, k apitypes.EnvironmentKind, l apitypes.DataLevel) apitypes.DataPolicyRule {
		return apitypes.DataPolicyRule{Subject: s, Kind: k, Level: l, Sensitive: masked}
	}
	for _, bad := range [][]apitypes.DataPolicyRule{
		{r("role:admin", prod, apitypes.LevelRead)},
		{r(apitypes.SubjectTokens, "staging", apitypes.LevelRead)},
		{r(apitypes.SubjectTokens, prod, "everything")},
		{r(apitypes.SubjectTokens, prod, apitypes.LevelRead), r(apitypes.SubjectTokens, prod, apitypes.LevelNone)},
	} {
		if err := ValidateRules(bad, "", ""); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	err := ValidateRules([]apitypes.DataPolicyRule{r(apitypes.SubjectTokens, nonProd, apitypes.LevelRead)}, prod, "connection orders")
	if err == nil || err.Error() != "connection orders is production; its data policy can only have production rules" {
		t.Fatalf("other kind: %v", err)
	}
	if err := ValidateRules([]apitypes.DataPolicyRule{r(apitypes.SubjectTokens, prod, apitypes.LevelRead)}, prod, "connection orders"); err != nil {
		t.Fatal(err)
	}
}
