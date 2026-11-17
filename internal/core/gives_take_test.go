package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// takeHarness is an org with two admins (ada, bo), a developer (dan), a
// viewer (vi) and lea: a developer who also manages members and roles
// (Members and access edit), without Full access.
type takeHarness struct {
	*harness
	ada, bo, dan, vi, lea Actor
}

func newTakeHarness(t *testing.T) *takeHarness {
	h := newHarness(t)
	h.enforce()
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	th := &takeHarness{harness: h,
		ada: h.user("ada@example.com", apitypes.ScopeAdmin), bo: h.user("bo@example.com", apitypes.ScopeAdmin),
		dan: h.user("dan@example.com", apitypes.ScopeDeploy), vi: h.user("vi@example.com", apitypes.ScopeRead),
	}
	lea := h.user("lea@example.com", apitypes.ScopeDeploy)
	if _, err := h.c.GrantMember(h.ctx, th.ada, lea.Email, apitypes.GrantRequest{Role: "members:edit"}); err != nil {
		t.Fatal(err)
	}
	th.lea = th.again(lea)
	return th
}

// again is a as its grants are now.
func (h *takeHarness) again(a Actor) Actor {
	h.t.Helper()
	out, err := h.c.actIn(h.ctx, userActor(h.userRow(a.UserID)), "", apitypes.ScopeAdmin)
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

// Taking access away is held to what the taker holds, like giving it
// (ADR 0080): someone who manages members without Full access doesn't
// demote an admin, take one out of Admins, end a grant, remove a group
// or thin out a role that holds more than they do. What they hold
// themselves, they take away.
func TestTakingAwayNeedsWhatIsTaken(t *testing.T) {
	h := newTakeHarness(t)
	if _, err := h.c.CreateRole(h.ctx, h.ada, apitypes.RoleRequest{Name: "Releaser", Includes: []string{"deploy:edit", "variables:reveal"}}); err != nil {
		t.Fatal(err)
	}
	g, err := h.c.GrantMember(h.ctx, h.ada, h.vi.Email, apitypes.GrantRequest{Role: "variables:reveal", Project: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	h.token(testSystem, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeRead})
	if _, err := h.c.SetGroupAgent(h.ctx, h.ada, "Admins", "ci", true); err != nil {
		t.Fatal(err)
	}
	for name, take := range map[string]func() error{
		"demoting an admin": func() error {
			_, err := h.c.SetMemberRole(h.ctx, h.lea, h.bo.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeRead})
			return err
		},
		"taking an admin out of Admins": func() error {
			_, err := h.c.RemoveGroupMember(h.ctx, h.lea, "Admins", h.bo.Email)
			return err
		},
		"taking a token out of Admins": func() error {
			_, err := h.c.SetGroupAgent(h.ctx, h.lea, "Admins", "ci", false)
			return err
		},
		"removing an admin":                      func() error { return h.c.RemoveMember(h.ctx, h.lea, h.bo.UserID) },
		"removing a member with a grant of more": func() error { return h.c.RemoveMember(h.ctx, h.lea, h.vi.UserID) },
		"ending a grant":                         func() error { return h.c.RevokeGrant(h.ctx, h.lea, g.ID) },
		"removing a group":                       func() error { return h.c.DeleteGroup(h.ctx, h.lea, "Approvers") },
		"thinning out a role": func() error {
			_, err := h.c.UpdateRole(h.ctx, h.lea, "Releaser", apitypes.RoleRequest{Name: "Releaser", Includes: []string{"deploy:edit"}})
			return err
		},
	} {
		if r := refusalOf(t, take()); r.DecidedBy != apitypes.RefusedGrants {
			t.Errorf("%s: refused by %s: %s", name, r.DecidedBy, r.Reason)
		}
	}
	// Nothing changed.
	if bo := h.again(h.bo); bo.Scope != apitypes.ScopeAdmin {
		t.Fatalf("bo is %s now", bo.Scope)
	}

	// What she holds, she takes away.
	if _, err := h.c.SetMemberRole(h.ctx, h.lea, h.dan.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeRead}); err != nil {
		t.Fatalf("demoting a developer: %v", err)
	}
	if _, err := h.c.RemoveGroupMember(h.ctx, h.lea, "Viewers", h.dan.Email); err != nil {
		t.Fatalf("taking a viewer out of Viewers: %v", err)
	}
	if err := h.c.RemoveMember(h.ctx, h.lea, h.dan.UserID); err != nil {
		t.Fatalf("removing a member who holds nothing more: %v", err)
	}
	// And an admin takes any of it away.
	if err := h.c.RevokeGrant(h.ctx, h.ada, g.ID); err != nil {
		t.Fatalf("an admin ending a grant: %v", err)
	}
	if _, err := h.c.SetMemberRole(h.ctx, h.ada, h.bo.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeRead}); err != nil {
		t.Fatalf("an admin demoting an admin: %v", err)
	}
}

// A role is its seeded group as the org keeps it: giving the role (a
// role change, an invite) gives what that group holds now, so it needs
// that, not only the built-in roles the group started with.
func TestARoleGivesWhatItsGroupHoldsNow(t *testing.T) {
	h := newTakeHarness(t)
	// The org lets its viewers reveal one project's secret values.
	grp, err := h.c.GrantGroup(h.ctx, h.ada, "Viewers", apitypes.GrantRequest{Role: "variables:reveal", Project: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	toViewer := func() error {
		_, err := h.c.SetMemberRole(h.ctx, h.lea, h.dan.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeRead})
		return err
	}
	invite := func() error {
		_, err := h.c.CreateInvite(h.ctx, h.lea, apitypes.CreateInviteRequest{Email: "new@example.com", Role: apitypes.ScopeRead})
		return err
	}
	for name, give := range map[string]func() error{"a role change": toViewer, "an invite": invite} {
		if r := refusalOf(t, give()); r.DecidedBy != apitypes.RefusedGrants {
			t.Errorf("%s into a group that holds more: refused by %s: %s", name, r.DecidedBy, r.Reason)
		}
	}
	for _, g := range grp.Grants {
		if g.Role == "variables:reveal" {
			if err := h.c.RevokeGrant(h.ctx, h.ada, g.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, give := range map[string]func() error{"a role change": toViewer, "an invite": invite} {
		if err := give(); err != nil {
			t.Errorf("%s into Viewers as it started: %v", name, err)
		}
	}
}
