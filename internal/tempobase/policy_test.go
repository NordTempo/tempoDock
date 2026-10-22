package tempobase

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// names lists the connections act sees.
func (h *harness) names(act Actor) []string {
	h.t.Helper()
	conns, err := h.ListConnections(h.ctx, act)
	if err != nil {
		h.t.Fatal(err)
	}
	out := make([]string, len(conns))
	for i, c := range conns {
		out[i] = c.Name
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The built-in policy: viewers read, editors write (confirming on
// production), tokens get nothing until granted and only propose on
// production. A connection at level none is invisible.
func TestPolicy(t *testing.T) {
	h := newHarness(t)
	admin := h.admin()
	h.connection("billing", true)
	h.connection("orders", false)
	viewer := h.user("viewer@example.com", tbtypes.RoleViewer)
	editor := h.user("editor@example.com", tbtypes.RoleEditor)

	all := []string{"billing", "orders"}
	if got := h.names(viewer); !equal(got, all) {
		t.Fatalf("viewer sees %v", got)
	}
	billing, err := h.GetConnection(h.ctx, editor, "billing")
	if err != nil {
		t.Fatal(err)
	}
	if p := billing.Access; p.Level != apitypes.LevelWrite || !p.ConfirmCommit || p.Approve || p.Manage {
		t.Fatalf("editor on production: %+v", p)
	}

	// Tokens follow the tokens rule: nothing, so nothing is visible.
	agent := h.token(editor, tbtypes.CreateTokenRequest{Name: "agent"})
	if got := h.names(agent); len(got) != 0 {
		t.Fatalf("ungranted token sees %v", got)
	}
	_, err = h.GetConnection(h.ctx, agent, "orders")
	wantCode(t, err, apitypes.CodeNotFound)

	// Its own access, capped by a viewer owner; write is propose on
	// production.
	grant := &apitypes.DataTokenAccess{Production: apitypes.LevelWrite, NonProduction: apitypes.LevelWrite, Sensitive: apitypes.SensitiveVisible}
	capped := h.token(viewer, tbtypes.CreateTokenRequest{Name: "capped", Data: grant})
	c, err := h.GetConnection(h.ctx, capped, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if c.Access.Level != apitypes.LevelRead || c.Access.Sensitive != apitypes.SensitiveMasked {
		t.Fatalf("viewer's token: %+v", c.Access)
	}
	strong := h.token(admin, tbtypes.CreateTokenRequest{Name: "strong", Data: grant, Connections: []string{"billing"}})
	c, err = h.GetConnection(h.ctx, strong, "billing")
	if err != nil {
		t.Fatal(err)
	}
	if p := c.Access; p.Level != apitypes.LevelPropose || p.Approve || p.Manage {
		t.Fatalf("admin's token on production: %+v", p)
	}
	if got := h.names(strong); !equal(got, []string{"billing"}) {
		t.Fatalf("limited token sees %v", got)
	}

	// A connection rule hides orders from viewers; the default policy is
	// untouched and admins still see everything.
	rules := []apitypes.DataPolicyRule{{Subject: apitypes.SubjectReadUsers, Kind: apitypes.KindNonProduction, Level: apitypes.LevelNone, Sensitive: apitypes.SensitiveMasked}}
	pol, err := h.SetDataPolicy(h.ctx, admin, "orders", apitypes.UpdateDataPolicyRequest{Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	if pol.Scope != policyConnection || len(pol.Rules) != 1 || len(pol.Effective) != 3 {
		t.Fatalf("policy %+v", pol)
	}
	if got := h.names(viewer); !equal(got, []string{"billing"}) {
		t.Fatalf("viewer sees %v", got)
	}
	_, err = h.GetDataPolicy(h.ctx, viewer, "orders")
	wantCode(t, err, apitypes.CodeNotFound)
	if got := h.names(admin); !equal(got, all) {
		t.Fatalf("admin sees %v", got)
	}

	// A connection's rules name its own kind only; only admins set them.
	rules[0].Kind = apitypes.KindProduction
	_, err = h.SetDataPolicy(h.ctx, admin, "orders", apitypes.UpdateDataPolicyRequest{Rules: rules})
	wantCode(t, err, apitypes.CodeValidation)
	_, err = h.SetDataPolicy(h.ctx, editor, "", apitypes.UpdateDataPolicyRequest{})
	wantCode(t, err, apitypes.CodeForbidden)
}
