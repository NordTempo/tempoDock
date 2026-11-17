package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// operator makes a platform operator who is a member of no org.
func (h *harness) operator(email string) Actor {
	h.t.Helper()
	a := h.member(email, nil)
	h.exec(`UPDATE users SET platform_role = 'operator' WHERE id = ?`, a.UserID)
	return a.withPlatform(apitypes.PlatformOperator)
}

// consoleOrg finds an org on the operator's list.
func (h *harness) consoleOrg(op Actor, slug string) (apitypes.OperatorOrg, bool) {
	h.t.Helper()
	all, err := h.c.OperatorOrgs(h.ctx, op, false)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, o := range all {
		if o.Slug == slug {
			return o, true
		}
	}
	return apitypes.OperatorOrg{}, false
}

// An operator who isn't a member sees every org's people and size, and
// may suspend it, fix its admins and stop its services, but not read its
// content; members and other users may do none of it.
func TestOperatorAdministersOrgsWithoutMembership(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	op := h.operator("op@x")
	admin := h.in(h.member("ada@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin}), "test")
	dev := h.in(h.member("dev@x", map[string]apitypes.Scope{"test": apitypes.ScopeDeploy}), "test")
	h.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeRead})

	o, ok := h.consoleOrg(op, "test")
	if !ok || o.Services != 1 || o.Projects != 1 || o.Owner != "ada@x" || o.Status != apitypes.OrgActive {
		t.Fatalf("test org = %+v", o)
	}
	if acme, _ := h.consoleOrg(op, "acme"); acme.Status != apitypes.OrgNoAdmin || len(acme.Admins) != 0 {
		t.Fatalf("acme = %+v; want no admin", acme)
	}
	for _, a := range []Actor{admin, dev} {
		if _, err := h.c.OperatorOrgs(h.ctx, a, false); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s listed every org: %v", a, err)
		}
		if _, err := h.c.SuspendOrg(h.ctx, a, "test", true); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s suspended the org: %v", a, err)
		}
	}
	// Not a member: the org's content is out of reach.
	if _, err := h.c.ListVariables(h.ctx, op, web.Env(), "web", false); err == nil {
		t.Fatal("the operator read the org's variables without joining")
	}

	// Suspended: reads work, deploys and changes don't, the app runs on.
	if o, err := h.c.SuspendOrg(h.ctx, op, "test", true); err != nil || o.Status != apitypes.OrgSuspended || o.SuspendedBy != "op@x" {
		t.Fatalf("suspend = %+v, %v", o, err)
	}
	dev = h.in(h.member("dev2@x", map[string]apitypes.Scope{"test": apitypes.ScopeDeploy}), "test")
	if _, err := h.c.GetService(h.ctx, dev, web); err != nil {
		t.Fatalf("a member can't read a suspended org: %v", err)
	}
	if _, err := h.c.DeployImage(h.ctx, dev, web, apitypes.DeployImageRequest{Image: "app:2"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("a member deployed in a suspended org: %v", err)
	}
	if _, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:2"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("a deploy outside a member's request (a push) went ahead: %v", err)
	}
	if s, _ := h.c.GetService(h.ctx, testSystem, web); s.ActiveRelease == nil || *s.ActiveRelease != 1 {
		t.Fatalf("service = %+v; release 1 keeps running", s)
	}
	if _, err := h.c.SuspendOrg(h.ctx, op, "test", false); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "app:2")

	// An org without an admin gets one from the operator.
	bob, err := h.store.GetUserByEmail(h.ctx, "bob@x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.OperatorSetMemberRole(h.ctx, op, "acme", bob.ID, apitypes.OperatorMemberRequest{Role: apitypes.ScopeAdmin}); err != nil {
		t.Fatal(err)
	}
	if acme, _ := h.consoleOrg(op, "acme"); acme.Status != apitypes.OrgActive || acme.Owner != "bob@x" {
		t.Fatalf("acme = %+v; want bob as its admin", acme)
	}
	if _, err := h.c.OperatorAddMember(h.ctx, op, "acme", apitypes.OperatorMemberRequest{Email: "ada@x", Role: apitypes.ScopeDeploy}); err != nil {
		t.Fatal(err)
	}

	if out, err := h.c.StopOrgServices(h.ctx, op, "test"); err != nil || out.Stopped != 1 {
		t.Fatalf("stop = %+v, %v", out, err)
	}
	if s, _ := h.c.GetService(h.ctx, testSystem, web); s.StoppedAt == nil {
		t.Fatalf("service = %+v; want stopped", s)
	}
	// The org's admins see what the operator did in their own log.
	evs, err := h.c.ListAudit(h.ctx, admin, AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e.Action] = true
	}
	if !seen["org.suspend"] || !seen["org.unsuspend"] || !seen["org.stop_services"] {
		t.Fatalf("the org's log = %v; want the operator's actions", seen)
	}
}

// Join as operator makes the operator an admin for a while, which the
// org's admins see; when it runs out the access ends at once, and the
// retention pass removes the membership and says so.
func TestJoinAsOperatorExpires(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	op := h.operator("op@x")
	h.member("ada@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin})

	grant, err := h.c.JoinOrgAsOperator(h.ctx, op, "test", apitypes.JoinOrgRequest{Hours: 2})
	if err != nil || grant.Role != apitypes.ScopeAdmin {
		t.Fatalf("join = %+v, %v", grant, err)
	}
	in := h.in(op, "test")
	if _, err := h.c.ListVariables(h.ctx, in, web.Env(), "web", false); err != nil {
		t.Fatalf("a joined operator can't read the org: %v", err)
	}
	if o, _ := h.consoleOrg(op, "test"); o.Members != 1 || len(o.Admins) != 1 {
		t.Fatalf("org = %+v; the operator isn't one of its own", o)
	}
	// An operator who joined may leave though the org has one admin of its own.
	if _, err := h.c.JoinOrgAsOperator(h.ctx, op, "test", apitypes.JoinOrgRequest{}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("joined twice: %v", err)
	}

	h.exec(`UPDATE org_members SET expires_at = unixepoch() - 1 WHERE user_id = ?`, op.UserID)
	if _, err := h.c.actIn(h.ctx, op, "test", apitypes.ScopeAdmin); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("an expired operator still acts in the org: %v", err)
	}
	h.c.expireOperatorMembers(h.ctx)
	var left int
	if err := h.store.DB.QueryRowContext(h.ctx, `SELECT count(*) FROM org_members WHERE user_id = ?`, op.UserID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("memberships left = %d, %v", left, err)
	}
	evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{})
	var joined, ended bool
	for _, e := range evs {
		joined = joined || e.Action == "org.operator_join"
		ended = ended || e.Action == "org.operator_leave"
	}
	if !joined || !ended {
		t.Fatal("the org's log doesn't show the operator joining and leaving")
	}
}

// Deleting an org with projects needs its slug typed, archives the
// projects and hides the org; its members lose it.
func TestOperatorDeletesOrg(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.deploy(web, "app:1")
	op := h.operator("op@x")
	ada := h.member("ada@x", map[string]apitypes.Scope{"test": apitypes.ScopeAdmin})

	if err := h.c.OperatorDeleteOrg(h.ctx, op, "test", "tset"); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("deleted without the right slug: %v", err)
	}
	if err := h.c.OperatorDeleteOrg(h.ctx, op, "test", "test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.consoleOrg(op, "test"); ok {
		t.Fatal("a deleted org is still listed")
	}
	if _, err := h.c.actIn(h.ctx, ada, "test", apitypes.ScopeAdmin); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("a member still acts in the deleted org: %v", err)
	}
	if p, err := h.c.GetProject(h.ctx, testSystem, "shop"); err != nil || p.ArchivedAt == nil {
		t.Fatalf("project = %+v, %v; want archived", p, err)
	}
}

// An operator creates an org with nobody in it and invites its people:
// it has no admin until an admin invite is accepted. Others may not.
func TestOperatorCreatesOrgAndInvites(t *testing.T) {
	h := newHarness(t)
	op := h.operator("op@x")
	ada := h.member("ada@x", nil)

	if _, err := h.c.OperatorCreateOrg(h.ctx, ada, apitypes.CreateOrgRequest{Slug: "acme"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a user created an org from the console: %v", err)
	}
	o, err := h.c.OperatorCreateOrg(h.ctx, op, apitypes.CreateOrgRequest{Slug: "acme", Name: "Acme"})
	if err != nil || o.Name != "Acme" || o.Members != 0 || o.Status != apitypes.OrgNoAdmin {
		t.Fatalf("create = %+v, %v; want an org with no members", o, err)
	}
	if _, err := h.c.OperatorCreateOrg(h.ctx, op, apitypes.CreateOrgRequest{Slug: "acme"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("created acme twice: %v", err)
	}
	if _, err := h.c.OperatorCreateInvite(h.ctx, ada, "acme", apitypes.CreateInviteRequest{Email: "ada@x", Role: apitypes.ScopeAdmin}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a non-member invited into acme: %v", err)
	}

	inv, err := h.c.OperatorCreateInvite(h.ctx, op, "acme", apitypes.CreateInviteRequest{Email: "ada@x", Role: apitypes.ScopeAdmin})
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.c.OperatorCreateInvite(h.ctx, op, "acme", apitypes.CreateInviteRequest{Email: "bob@x", Role: apitypes.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.c.OperatorRevokeInvite(h.ctx, op, "acme", other.Invite.ID); err != nil {
		t.Fatal(err)
	}
	if open, err := h.c.OperatorInvites(h.ctx, op, "acme"); err != nil || len(open) != 1 || open[0].Email != "ada@x" {
		t.Fatalf("invites = %+v, %v; want ada's", open, err)
	}
	if _, err := h.c.AcceptInvite(h.ctx, ada, inv.Code); err != nil {
		t.Fatal(err)
	}
	if o, _ := h.consoleOrg(op, "acme"); o.Status != apitypes.OrgActive || o.Owner != "ada@x" {
		t.Fatalf("acme = %+v; want ada as its admin", o)
	}
}

// An operator administers an org with the platform's permission, not
// with what they hold as a member somewhere: one who only views their
// own org still changes another's members.
func TestOperatorActsWithoutTheirOwnOrgsGrants(t *testing.T) {
	h := newHarness(t)
	h.enforce()
	h.member("ada@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin})
	bob := h.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeRead})
	op := h.member("op@x", map[string]apitypes.Scope{"test": apitypes.ScopeRead})
	h.exec(`UPDATE users SET platform_role = 'operator' WHERE id = ?`, op.UserID)
	op = h.in(op.withPlatform(apitypes.PlatformOperator), "test")
	if !op.hasGrants || op.Scope != apitypes.ScopeRead {
		t.Fatalf("setup: the operator in their own org = %+v", op.Me())
	}
	u, err := h.c.OperatorSetMemberRole(h.ctx, op, "acme", bob.UserID, apitypes.OperatorMemberRequest{Role: apitypes.ScopeDeploy})
	if err != nil || u.Role != apitypes.ScopeDeploy {
		t.Fatalf("an operator who views their own org changing a role in another: %+v, %v", u, err)
	}
}

// What an operator does to an org's people and to the org itself is as
// sensitive as the org's own member changes (ADR 0057): joining it,
// adding a member, suspending and deleting it need a session that proved
// who it is within the last minutes. And a token adds no member while an
// environment of the org is locked, as the org's own tokens don't.
func TestOperatorOrgChangesNeedARecentSignIn(t *testing.T) {
	h := newHarness(t)
	h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	h.member("ada@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin})
	h.member("bob@x", nil)
	stale := h.operator("op@x")
	stale.SessionID, stale.VerifiedAt = 99, now().Add(-2*ReauthWindow)
	fresh := stale
	fresh.VerifiedAt = now()
	for name, do := range map[string]func(Actor) error{
		"joining": func(a Actor) error {
			_, err := h.c.JoinOrgAsOperator(h.ctx, a, "acme", apitypes.JoinOrgRequest{})
			return err
		},
		"adding a member": func(a Actor) error {
			_, err := h.c.OperatorAddMember(h.ctx, a, "acme", apitypes.OperatorMemberRequest{Email: "bob@x", Role: apitypes.ScopeAdmin})
			return err
		},
		"suspending": func(a Actor) error { _, err := h.c.SuspendOrg(h.ctx, a, "acme", true); return err },
		"deleting":   func(a Actor) error { return h.c.OperatorDeleteOrg(h.ctx, a, "acme", "acme") },
	} {
		wantCode(t, name+" with a stale session", do(stale), apitypes.CodeReauthRequired)
	}
	if _, err := h.c.JoinOrgAsOperator(h.ctx, fresh, "acme", apitypes.JoinOrgRequest{}); err != nil {
		t.Fatalf("joining, just signed in: %v", err)
	}

	h.lock("production")
	pt, err := h.c.CreatePlatformToken(h.ctx, System, apitypes.CreatePlatformTokenRequest{Name: "ops", Role: apitypes.PlatformOperator})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := h.c.AuthenticateToken(h.ctx, pt.Secret, "test")
	if err != nil {
		t.Fatal(err)
	}
	add := apitypes.OperatorMemberRequest{Email: "bob@x", Role: apitypes.ScopeAdmin}
	if _, err := h.c.OperatorAddMember(h.ctx, tok, "test", add); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("a platform token adding an admin to an org with a locked environment: %v", err)
	}
	if _, err := h.c.OperatorAddMember(h.ctx, tok, "acme", add); err != nil {
		t.Fatalf("a platform token adding a member to an org with no lock: %v", err)
	}
}
