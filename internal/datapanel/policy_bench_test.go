package datapanel

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// BenchmarkDecide is one permission decision as every data request makes
// it: an agent token with rules at three scopes and two caps.
func BenchmarkDecide(b *testing.B) {
	var rules []apitypes.DataPolicyRule
	for _, from := range []apitypes.DataPolicyScope{apitypes.PolicyDefault, apitypes.PolicyProject, apitypes.PolicyEnvironment} {
		for _, s := range Subjects {
			for _, k := range Kinds {
				rules = append(rules, rule(from, s, k, apitypes.LevelRead, masked))
			}
		}
	}
	in := PolicyInput{
		Kind: prod, Agent: true, Subject: apitypes.SubjectDeployUsers, Rules: rules, BuiltIn: tbBuiltIn,
		ScopeName: func(s apitypes.DataPolicyScope) string { return "project shop" },
		Token: &TokenInput{Name: "bot", Caps: []Cap{
			{Subject: apitypes.SubjectDeployUsers, Why: "capped by the token's scope deploy: "},
			{Subject: apitypes.SubjectReadUsers, Why: "capped by its creator's current role read: "},
		}},
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = Decide(in)
	}
}
