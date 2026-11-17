package core

import (
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// An invite works once, for the email it names, until it expires or is
// revoked, and makes that person a member with its role.
func TestInvites(t *testing.T) {
	h := newHarness(t)
	admin := h.in(h.member("ada@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	bob := h.member("Bob@x", nil)
	carol := h.member("carol@x", nil)

	inv, err := h.c.CreateInvite(h.ctx, admin, apitypes.CreateInviteRequest{Email: "bob@x", Role: apitypes.ScopeDeploy})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := h.c.Invite(h.ctx, inv.Code, "192.0.2.1"); err != nil || info.Org != "test" || info.Role != apitypes.ScopeDeploy || info.InvitedBy != "ada@x" {
		t.Fatalf("invite = %+v, %v", info, err)
	}
	if _, err := h.c.AcceptInvite(h.ctx, carol, inv.Code); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("someone else accepting: %v", err)
	}
	if _, err := h.c.AcceptInvite(h.ctx, bob, inv.Code); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if in := h.in(bob, "test"); in.Scope != apitypes.ScopeDeploy {
		t.Fatalf("bob in test: %+v", in)
	}
	if _, err := h.c.AcceptInvite(h.ctx, bob, inv.Code); CodeOf(err) != apitypes.CodeExpired {
		t.Fatalf("second use: %v", err)
	}
	if _, err := h.c.CreateInvite(h.ctx, admin, apitypes.CreateInviteRequest{Email: "bob@x", Role: apitypes.ScopeRead}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("inviting a member: %v", err)
	}

	revoked, _ := h.c.CreateInvite(h.ctx, admin, apitypes.CreateInviteRequest{Email: "carol@x", Role: apitypes.ScopeRead})
	if err := h.c.RevokeInvite(h.ctx, admin, revoked.Invite.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AcceptInvite(h.ctx, carol, revoked.Code); CodeOf(err) != apitypes.CodeExpired {
		t.Fatalf("revoked: %v", err)
	}
	late, _ := h.c.CreateInvite(h.ctx, admin, apitypes.CreateInviteRequest{Email: "carol@x", Role: apitypes.ScopeRead})
	if _, err := h.store.DB.ExecContext(h.ctx, "UPDATE org_invites SET expires_at = ? WHERE id = ?", time.Now().Add(-time.Minute).UTC(), late.Invite.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AcceptInvite(h.ctx, carol, late.Code); CodeOf(err) != apitypes.CodeExpired {
		t.Fatalf("expired: %v", err)
	}
	if list, _ := h.c.ListInvites(h.ctx, admin); len(list) != 1 || !list[0].Expired {
		t.Fatalf("open invites = %+v; want the expired one", list)
	}
	if _, err := h.c.CreateInvite(h.ctx, h.in(bob, "test"), apitypes.CreateInviteRequest{Email: "eve@x", Role: apitypes.ScopeAdmin}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a deploy member inviting: %v", err)
	}
}

// Roles change and members go, but an org always keeps an admin.
func TestMembersKeepAnAdmin(t *testing.T) {
	h := newHarness(t)
	ada := h.member("ada@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin})
	bob := h.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeRead})
	admin := h.in(ada, "acme")

	if _, err := h.c.SetMemberRole(h.ctx, admin, ada.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeRead}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("last admin stepping down: %v", err)
	}
	if err := h.c.LeaveOrg(h.ctx, admin); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("last admin leaving: %v", err)
	}
	if _, err := h.c.SetMemberRole(h.ctx, admin, bob.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeAdmin}); err != nil {
		t.Fatal(err)
	}
	if err := h.c.LeaveOrg(h.ctx, admin); err != nil {
		t.Fatalf("leaving with another admin: %v", err)
	}
	if _, err := h.c.actIn(h.ctx, ada, "acme", apitypes.ScopeAdmin); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("ada after leaving: %v", err)
	}
	if err := h.c.RemoveMember(h.ctx, h.in(bob, "acme"), bob.UserID); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("removing yourself: %v", err)
	}
}

// ${{org.KEY}} resolves to the org's value in every project; where it is
// used shows, it can't be removed while used, and a redeploy rolls a new
// value out.
func TestOrgVariables(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if _, err := h.c.SetOrgVariable(h.ctx, testSystem, "SENTRY_DSN", "https://one"); err != nil {
		t.Fatal(err)
	}
	h.setVars(web, map[string]string{"DSN": "${{org.SENTRY_DSN}}"})
	rel := h.deploy(web, "app:1")
	if spec, _ := h.rt.Spec(h.ctr(web, rel.Number)); spec.Env["DSN"] != "https://one" {
		t.Fatalf("DSN = %q", spec.Env["DSN"])
	}

	vars, err := h.c.ListOrgVariables(h.ctx, testSystem, false)
	if err != nil || len(vars) != 1 || vars[0].Value != nil || len(vars[0].UsedBy) != 1 || vars[0].UsedBy[0].Service != "web" {
		t.Fatalf("org variables = %+v, %v", vars, err)
	}
	if err := h.c.DeleteOrgVariable(h.ctx, testSystem, "SENTRY_DSN"); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "shop/production/web") {
		t.Fatalf("removing one in use: %v", err)
	}

	if _, err := h.c.SetOrgVariable(h.ctx, testSystem, "SENTRY_DSN", "https://two"); err != nil {
		t.Fatal(err)
	}
	out, err := h.c.RedeployOrgVariable(h.ctx, testSystem, "SENTRY_DSN")
	if err != nil || len(out.Started) != 1 || len(out.Skipped) != 0 {
		t.Fatalf("redeploy = %+v, %v", out, err)
	}
	h.wait(web, out.Started[0].Number)
	if spec, _ := h.rt.Spec(h.ctr(web, out.Started[0].Number)); spec.Env["DSN"] != "https://two" {
		t.Fatalf("DSN after redeploy = %q", spec.Env["DSN"])
	}

	reader := h.in(h.member("ro@x", map[string]apitypes.Scope{"test": apitypes.ScopeRead}), "test")
	if _, err := h.c.SetOrgVariable(h.ctx, reader, "X", "1"); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a reader setting one: %v", err)
	}
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: map[string]string{"Y": "${{org.MISSING}}"}, Apply: true}); err == nil || !strings.Contains(err.Error(), "organization has no variable MISSING") {
		t.Fatalf("a missing org variable: %v", err)
	}
	if _, err := h.c.CreateService(h.ctx, testSystem, web.Env(), apitypes.CreateServiceRequest{Name: "org"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("a service named org: %v", err)
	}
}

// Only an org without projects can be deleted; its tokens stop working.
func TestDeleteOrg(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	if err := h.c.DeleteOrg(h.ctx, testSystem); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("deleting an org with projects: %v", err)
	}
	ada := h.in(h.member("ada@x", map[string]apitypes.Scope{"empty": apitypes.ScopeAdmin}), "empty")
	tok, err := h.c.CreateToken(h.ctx, ada, apitypes.CreateTokenRequest{Name: "ci", Scope: apitypes.ScopeDeploy})
	if err != nil {
		t.Fatal(err)
	}
	// Its status pages go first, with the records Dock made for them.
	if _, err := h.c.CreateStatusPage(h.ctx, ada, apitypes.CreateStatusPageRequest{Slug: "empty"}); err != nil {
		t.Fatal(err)
	}
	if err := h.c.DeleteOrg(h.ctx, ada); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("deleting an org with a status page: %v", err)
	}
	if err := h.c.DeleteStatusPage(h.ctx, ada, "empty"); err != nil {
		t.Fatal(err)
	}
	if err := h.c.DeleteOrg(h.ctx, ada); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.AuthenticateToken(h.ctx, tok.Secret, ""); CodeOf(err) != apitypes.CodeUnauthorized {
		t.Fatalf("the deleted org's token: %v", err)
	}
	if _, err := h.store.GetMembership(h.ctx, store.GetMembershipParams{UserID: ada.UserID, Slug: "empty"}); !store.IsNotFound(err) {
		t.Fatalf("membership after delete: %v", err)
	}
}

// Leaving an organization is the person's own act, signed in: a token
// that acts for them (their `dock login`, which an agent runs with)
// doesn't end their membership.
func TestOnlyThePersonLeaves(t *testing.T) {
	h := newHarness(t)
	h.user("ada@example.com", apitypes.ScopeAdmin)
	dev := h.cliUser("dev@example.com", apitypes.ScopeRead)
	bot, err := h.c.AuthenticateToken(h.ctx, h.cliToken(dev), "")
	if err != nil {
		t.Fatal(err)
	}
	if r := refusalOf(t, h.c.LeaveOrg(h.ctx, bot)); r.DecidedBy != apitypes.RefusedPerson {
		t.Fatalf("their `dock login` leaving for them: %+v", r)
	}
	if err := h.c.LeaveOrg(h.ctx, dev); err != nil {
		t.Fatalf("the person leaving: %v", err)
	}
}

// The open invites a member made end when they leave, are removed or
// hold a lower role: nobody is left to vouch for the roles they offered.
func TestInvitesEndWithTheirMakersRole(t *testing.T) {
	h := newHarness(t)
	root := h.user("root@example.com", apitypes.ScopeAdmin)
	open := func() []string {
		t.Helper()
		invs, err := h.c.ListInvites(h.ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, i := range invs {
			out = append(out, i.Email)
		}
		return out
	}
	if _, err := h.c.CreateInvite(h.ctx, root, apitypes.CreateInviteRequest{Email: "kept@example.com", Role: apitypes.ScopeRead}); err != nil {
		t.Fatal(err)
	}
	for name, end := range map[string]func(a Actor) error{
		"leaving": func(a Actor) error { return h.c.LeaveOrg(h.ctx, a) },
		"removed": func(a Actor) error { return h.c.RemoveMember(h.ctx, root, a.UserID) },
		"demoted": func(a Actor) error {
			_, err := h.c.SetMemberRole(h.ctx, root, a.UserID, apitypes.SetMemberRoleRequest{Role: apitypes.ScopeDeploy})
			return err
		},
	} {
		admin := h.user(name+"@example.com", apitypes.ScopeAdmin)
		if _, err := h.c.CreateInvite(h.ctx, admin, apitypes.CreateInviteRequest{Email: "friend-of-" + name + "@example.com", Role: apitypes.ScopeAdmin}); err != nil {
			t.Fatal(err)
		}
		if got := open(); len(got) != 2 {
			t.Fatalf("%s: open invites before = %v", name, got)
		}
		if err := end(admin); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := open(); len(got) != 1 || got[0] != "kept@example.com" {
			t.Fatalf("%s: open invites after = %v; want only root's", name, got)
		}
	}
}
