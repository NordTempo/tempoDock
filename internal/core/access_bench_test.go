package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// BenchmarkCheckEnvShadowed is one environment check as every request
// makes it, with the permission model's shadow beside it (ADR 0080): a
// table lookup and a few bit tests, no query and no allocation.
func BenchmarkCheckEnvShadowed(b *testing.B) {
	p := store.Project{ID: 7, OrgID: 1, Name: "shop"}
	e := store.Environment{ID: 9, ProjectID: 7, Name: "staging"}
	a := Actor{Kind: "token", Name: "ci", OrgID: 1, OrgSlug: "default", Scope: apitypes.ScopeDeploy,
		ProjectID: 7, ProjectName: "shop", EnvironmentID: 9, EnvironmentName: "staging"}
	b.ReportAllocs()
	for b.Loop() {
		if err := a.checkEnvAccess(apitypes.ScopeDeploy, "deploying", p, e); err != nil {
			b.Fatal(err)
		}
	}
}
