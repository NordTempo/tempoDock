package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Setting a variable to its live value un-stages it, which the count of
// staged changes shows. Someone who edits variables without revealing
// them isn't told that way whether a value they try is the live one:
// theirs is staged whatever is live.
func TestSettingAVariableDoesNotTellItsValue(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"ADMIN_PIN": "4711"}, Apply: true}); err != nil {
		t.Fatal(err)
	}
	admin := h.user("ada@example.com", apitypes.ScopeAdmin)
	editor := grantedPerson(h, admin, "ed@example.com", apitypes.GrantRequest{Role: "deploy:view"}, apitypes.GrantRequest{Role: "variables:edit"})
	if _, err := h.c.ListVariables(h.ctx, editor, web.Env(), "", true); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("variables:edit revealing: %v", err)
	}
	guess := func(v string) int {
		res, err := h.c.SetVariables(h.ctx, editor, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"ADMIN_PIN": v}})
		if err != nil {
			t.Fatal(err)
		}
		return res.Pending.Count
	}
	if wrong, right := guess("0000"), guess("4711"); wrong != 1 || right != 1 {
		t.Errorf("a wrong value stages %d change(s) and the live one %d for someone who can't reveal: the count tells which it was", wrong, right)
	}
	// Someone who may reveal still un-stages by setting the live value.
	if res, err := h.c.SetVariables(h.ctx, admin, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"ADMIN_PIN": "4711"}}); err != nil || res.Pending.Count != 0 {
		t.Errorf("an admin setting the live value: %+v, %v", res.Pending, err)
	}
}
