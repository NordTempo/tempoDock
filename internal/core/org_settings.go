package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// An org's settings (ADR 0027 phase 2): its name, members, invites and
// variables. Changing them needs an unlimited admin of the org; leaving
// and accepting an invite are the user's own.

// InviteTTL is how long an invite link works.
const InviteTTL = 7 * 24 * time.Hour

// invitePrefix marks invite codes, like dock_ marks tokens.
const invitePrefix = "dinv_"

// GetOrg returns the org the actor acts in, with its role there.
func (c *Core) GetOrg(ctx context.Context, a Actor) (apitypes.Org, error) {
	if err := a.require(apitypes.ScopeRead, "reading the organization"); err != nil {
		return apitypes.Org{}, err
	}
	o, err := c.store.GetOrg(ctx, a.OrgID)
	if err != nil {
		return apitypes.Org{}, err
	}
	return toOrg(o, a.Scope), nil
}

// RenameOrg changes the org's display name; its slug (its address) stays.
func (c *Core) RenameOrg(ctx context.Context, a Actor, req apitypes.UpdateOrgRequest) (apitypes.Org, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "renaming the organization"); err != nil {
		return apitypes.Org{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return apitypes.Org{}, invalid("the organization's name can't be empty")
	}
	o, err := c.store.RenameOrg(ctx, store.RenameOrgParams{Name: name, ID: a.OrgID})
	if err != nil {
		return apitypes.Org{}, err
	}
	c.audit(ctx, a, "org.rename", nil, nil, o.Slug, "renamed the organization to "+name)
	return toOrg(o, a.Scope), nil
}

// DeleteOrg deletes an org that has no projects (archived ones count:
// projects are never deleted), with its memberships, invites, variables,
// registry credentials and data policy, and revokes its tokens. Its audit
// log stays.
func (c *Core) DeleteOrg(ctx context.Context, a Actor) error {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "deleting the organization"); err != nil {
		return err
	}
	n, err := c.store.CountOrgProjects(ctx, a.OrgID)
	if err != nil {
		return err
	}
	if n > 0 {
		return conflict("organization %s has %d projects (archived ones count); only an organization without projects can be deleted", a.OrgSlug, n)
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		for _, del := range []func(context.Context, int64) error{q.DeleteOrgMembers, q.DeleteOrgInvites, q.DeleteOrgVariables, q.DeleteOrgRegistryCredentials, q.DeleteOrgDataPolicy} {
			if err := del(ctx, a.OrgID); err != nil {
				return err
			}
		}
		if err := q.RevokeOrgTokens(ctx, store.RevokeOrgTokensParams{Now: ptr(now()), OrgID: &a.OrgID}); err != nil {
			return err
		}
		return q.DeleteOrg(ctx, a.OrgID)
	})
	if err != nil {
		return err
	}
	c.audit(ctx, a.platform(), "org.delete", nil, nil, a.OrgSlug, "deleted organization "+a.OrgSlug)
	return nil
}

// LeaveOrg takes the calling user out of the org. The last admin can't
// leave; another member becomes admin first.
func (c *Core) LeaveOrg(ctx context.Context, a Actor) error {
	if a.UserID == 0 {
		return errf(apitypes.CodeForbidden, "%s isn't a member; only users leave organizations", a)
	}
	if err := a.require(apitypes.ScopeRead, "leaving the organization"); err != nil {
		return err
	}
	if err := c.keepAnAdmin(ctx, a.OrgID, a.UserID, ""); err != nil {
		return err
	}
	if _, err := c.store.RemoveOrgMember(ctx, store.RemoveOrgMemberParams{OrgID: a.OrgID, UserID: a.UserID}); err != nil {
		return err
	}
	c.audit(ctx, a, "org.leave", nil, nil, a.Email, a.Email+" left the organization")
	return nil
}

// SetMemberRole changes a member's role. The org always keeps an admin.
func (c *Core) SetMemberRole(ctx context.Context, a Actor, userID int64, req apitypes.SetMemberRoleRequest) (apitypes.User, error) {
	u, err := c.member(ctx, a, userID, "changing roles")
	if err != nil {
		return apitypes.User{}, err
	}
	if err := c.checkAccountLock(ctx, a, "changing roles", false); err != nil {
		return apitypes.User{}, err
	}
	if !validScope(req.Role) {
		return apitypes.User{}, invalid("role must be read, deploy or admin")
	}
	if err := c.keepAnAdmin(ctx, a.OrgID, userID, req.Role); err != nil {
		return apitypes.User{}, err
	}
	if _, err := c.store.SetMemberRole(ctx, store.SetMemberRoleParams{Role: string(req.Role), OrgID: a.OrgID, UserID: userID}); err != nil {
		return apitypes.User{}, err
	}
	c.audit(ctx, a, "org.member_role", nil, nil, u.Email, fmt.Sprintf("made %s %s", u.Email, req.Role))
	return toUser(u, string(req.Role)), nil
}

// RemoveMember takes a member out of the org; their account stays. Use
// LeaveOrg for yourself.
func (c *Core) RemoveMember(ctx context.Context, a Actor, userID int64) error {
	u, err := c.member(ctx, a, userID, "removing members")
	if err != nil {
		return err
	}
	if userID == a.UserID {
		return invalid("to take yourself out of the organization, leave it")
	}
	if _, err := c.store.RemoveOrgMember(ctx, store.RemoveOrgMemberParams{OrgID: a.OrgID, UserID: userID}); err != nil {
		return err
	}
	c.audit(ctx, a, "org.member_remove", nil, nil, u.Email, "removed "+u.Email+" from the organization")
	return nil
}

// member loads a member of the actor's org for an admin action.
func (c *Core) member(ctx context.Context, a Actor, userID int64, action string) (store.User, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, action); err != nil {
		return store.User{}, err
	}
	if err := a.requireRecent(action); err != nil {
		return store.User{}, err
	}
	_, err := c.store.GetMemberRole(ctx, store.GetMemberRoleParams{OrgID: a.OrgID, UserID: userID})
	if store.IsNotFound(err) {
		return store.User{}, notFound("user %d isn't a member of %s", userID, a.OrgSlug)
	}
	if err != nil {
		return store.User{}, err
	}
	return c.store.GetUser(ctx, userID)
}

// keepAnAdmin refuses to leave the org without an admin when userID stops
// being one (next: their new role; "" for leaving).
func (c *Core) keepAnAdmin(ctx context.Context, orgID, userID int64, next apitypes.Scope) error {
	if next == apitypes.ScopeAdmin {
		return nil
	}
	role, err := c.store.GetMemberRole(ctx, store.GetMemberRoleParams{OrgID: orgID, UserID: userID})
	if err != nil || role != string(apitypes.ScopeAdmin) {
		return err
	}
	n, err := c.store.CountOrgAdmins(ctx, orgID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return conflict("an organization needs an admin; make another member admin first")
	}
	return nil
}

// CreateInvite invites an email address into the org with a role. The
// link's code is returned once; it works once, for InviteTTL, and only
// for a user signed in with that email.
func (c *Core) CreateInvite(ctx context.Context, a Actor, req apitypes.CreateInviteRequest) (apitypes.CreatedInvite, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "inviting people"); err != nil {
		return apitypes.CreatedInvite{}, err
	}
	if err := a.requireRecent("inviting people"); err != nil {
		return apitypes.CreatedInvite{}, err
	}
	if err := c.checkAccountLock(ctx, a, "inviting people", false); err != nil {
		return apitypes.CreatedInvite{}, err
	}
	email := strings.TrimSpace(req.Email)
	if !strings.Contains(email, "@") {
		return apitypes.CreatedInvite{}, invalid("email %q is not valid", req.Email)
	}
	if !validScope(req.Role) {
		return apitypes.CreatedInvite{}, invalid("role must be read, deploy or admin")
	}
	if _, err := c.store.GetMemberRoleByEmail(ctx, store.GetMemberRoleByEmailParams{OrgID: a.OrgID, Email: email}); err == nil {
		return apitypes.CreatedInvite{}, conflict("%s is a member of %s already", email, a.OrgSlug)
	}
	code, hash := newSecret(invitePrefix)
	n := now()
	inv, err := c.store.CreateOrgInvite(ctx, store.CreateOrgInviteParams{
		OrgID: a.OrgID, Email: email, Role: string(req.Role), Hash: hash, CreatedBy: a.Name, CreatedAt: n, ExpiresAt: n.Add(InviteTTL),
	})
	if err != nil {
		return apitypes.CreatedInvite{}, err
	}
	c.audit(ctx, a, "org.invite", nil, nil, email, fmt.Sprintf("invited %s as %s", email, req.Role))
	out := apitypes.CreatedInvite{Invite: toInvite(inv), Code: code}
	if u := c.publicURL(); u != "" {
		out.URL = u + "/invite/" + code
	}
	return out, nil
}

// ListInvites returns the org's open invites, expired ones too.
func (c *Core) ListInvites(ctx context.Context, a Actor) ([]apitypes.Invite, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "listing invites"); err != nil {
		return nil, err
	}
	rows, err := c.store.ListOrgInvites(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.Invite, len(rows))
	for i, r := range rows {
		out[i] = toInvite(r)
	}
	return out, nil
}

// RevokeInvite stops an open invite's link working.
func (c *Core) RevokeInvite(ctx context.Context, a Actor, id int64) error {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "revoking invites"); err != nil {
		return err
	}
	inv, err := c.store.GetOrgInvite(ctx, store.GetOrgInviteParams{ID: id, OrgID: a.OrgID})
	if store.IsNotFound(err) {
		return notFound("invite %d not found", id)
	}
	if err != nil {
		return err
	}
	if _, err := c.store.RevokeOrgInvite(ctx, store.RevokeOrgInviteParams{RevokedAt: ptr(now()), ID: id, OrgID: a.OrgID}); err != nil {
		return err
	}
	c.audit(ctx, a, "org.invite_revoke", nil, nil, inv.Email, "revoked the invite of "+inv.Email)
	return nil
}

// Invite shows an invite to the user who opened its link.
func (c *Core) Invite(ctx context.Context, a Actor, code string) (apitypes.InviteInfo, error) {
	row, err := c.invite(ctx, a, code)
	if err != nil {
		return apitypes.InviteInfo{}, err
	}
	inv := row.OrgInvite
	status := "open"
	switch {
	case inv.AcceptedAt != nil:
		status = "accepted"
	case inv.RevokedAt != nil:
		status = "revoked"
	case now().After(inv.ExpiresAt):
		status = "expired"
	}
	return apitypes.InviteInfo{
		Org: row.Org.Slug, OrgName: row.Org.Name, Email: inv.Email, Role: apitypes.Scope(inv.Role), ExpiresAt: inv.ExpiresAt, Status: status,
	}, nil
}

// AcceptInvite makes the calling user a member with the invite's role,
// if it is open and for their email.
func (c *Core) AcceptInvite(ctx context.Context, a Actor, code string) (apitypes.Org, error) {
	row, err := c.invite(ctx, a, code)
	if err != nil {
		return apitypes.Org{}, err
	}
	inv, org := row.OrgInvite, row.Org
	switch {
	case inv.AcceptedAt != nil || inv.RevokedAt != nil:
		return apitypes.Org{}, errf(apitypes.CodeExpired, "this invite was used or revoked; ask an admin of %s for a new one", org.Name)
	case now().After(inv.ExpiresAt):
		return apitypes.Org{}, errf(apitypes.CodeExpired, "this invite expired; ask an admin of %s for a new one", org.Name)
	case !strings.EqualFold(inv.Email, a.Email):
		return apitypes.Org{}, errf(apitypes.CodeForbidden, "this invite is for %s, and you are signed in as %s", inv.Email, a.Email)
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error { return joinTx(ctx, q, org, &inv, a.UserID, inv.Role) })
	if store.IsUnique(err) {
		return apitypes.Org{}, conflict("you are a member of %s already", org.Name)
	}
	if err != nil {
		return apitypes.Org{}, err
	}
	in := a.member(org, inv.Role, apitypes.ScopeAdmin)
	c.audit(ctx, in, "org.join", nil, nil, a.Email, fmt.Sprintf("%s joined as %s", a.Email, inv.Role))
	return toOrg(org, apitypes.Scope(inv.Role)), nil
}

// joinTx makes user userID a member of org with role, in transaction q,
// spending invite inv (nil for none): AcceptInvite, and signing in
// through single sign-on (ADR 0058).
func joinTx(ctx context.Context, q *store.Queries, org store.Org, inv *store.OrgInvite, userID int64, role string) error {
	if inv != nil {
		n, err := q.AcceptOrgInvite(ctx, store.AcceptOrgInviteParams{AcceptedAt: ptr(now()), ID: inv.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return errf(apitypes.CodeExpired, "this invite was used or revoked; ask an admin of %s for a new one", org.Name)
		}
	}
	return q.AddOrgMember(ctx, store.AddOrgMemberParams{OrgID: org.ID, UserID: userID, Role: role})
}

// invite loads an invite by its code, for a signed-in user.
func (c *Core) invite(ctx context.Context, a Actor, code string) (store.GetInviteByHashRow, error) {
	if a.UserID == 0 || a.Email == "" {
		return store.GetInviteByHashRow{}, errf(apitypes.CodeForbidden, "invites are accepted by people; sign in as the person invited")
	}
	if !strings.HasPrefix(code, invitePrefix) {
		return store.GetInviteByHashRow{}, notFound("invite not found; check the link")
	}
	row, err := c.store.GetInviteByHash(ctx, hashSecret(code))
	if store.IsNotFound(err) {
		return row, notFound("invite not found; check the link")
	}
	return row, err
}

func toInvite(i store.OrgInvite) apitypes.Invite {
	return apitypes.Invite{
		ID: i.ID, Email: i.Email, Role: apitypes.Scope(i.Role), CreatedBy: i.CreatedBy,
		CreatedAt: i.CreatedAt, ExpiresAt: i.ExpiresAt, Expired: now().After(i.ExpiresAt),
	}
}

// orgScope is the reference prefix of org variables: ${{org.KEY}}. No
// service may be named like it.
const orgScope = "org"

// ListOrgVariables returns the org's variables with where each is used;
// values only with reveal (admins).
func (c *Core) ListOrgVariables(ctx context.Context, a Actor, reveal bool) ([]apitypes.OrgVariable, error) {
	if err := a.require(apitypes.ScopeRead, "listing organization variables"); err != nil {
		return nil, err
	}
	if reveal {
		if err := a.require(apitypes.ScopeAdmin, "revealing secret values"); err != nil {
			return nil, err
		}
		if err := a.requireRecent("revealing secret values"); err != nil {
			return nil, err
		}
	}
	rows, err := c.store.ListOrgVariables(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	uses, err := c.orgVariableUses(ctx, a.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.OrgVariable, len(rows))
	for i, r := range rows {
		out[i] = apitypes.OrgVariable{Key: r.Key, UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedBy, UsedBy: uses[r.Key]}
		if out[i].UsedBy == nil {
			out[i].UsedBy = []apitypes.VariableUse{}
		}
		if reveal {
			if out[i].Value, err = c.openValue(r.Value, orgVariableAAD(r.OrgID, r.Key), "org."+r.Key); err != nil {
				return nil, err
			}
		}
	}
	if reveal {
		c.audit(ctx, a, "org.variables_reveal", nil, nil, a.OrgSlug, "revealed the organization's variable values")
	}
	return out, nil
}

// orgVariableUses finds the ${{org.KEY}} references in the live and staged
// variables of the org's live projects, by key.
func (c *Core) orgVariableUses(ctx context.Context, orgID int64) (map[string][]apitypes.VariableUse, error) {
	rows, err := c.store.ListOrgVariableUses(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := map[string][]apitypes.VariableUse{}
	for _, r := range rows {
		use := apitypes.VariableUse{Project: r.ProjectName, Environment: r.EnvironmentName, Service: r.ServiceName}
		for _, sealed := range [][]byte{r.Value, r.PendingValue} {
			v, err := c.openValue(sealed, variableAAD(r.EnvironmentID, r.ServiceID, r.Key), r.Key)
			if err != nil {
				return nil, err
			}
			if v == nil {
				continue
			}
			for _, m := range refRe.FindAllStringSubmatch(*v, -1) {
				if m[1] == orgScope && !slices.Contains(out[m[2]], use) {
					out[m[2]] = append(out[m[2]], use)
				}
			}
		}
	}
	return out, nil
}

// SetOrgVariable sets an org variable. Services that reference it get the
// new value on their next release (RedeployOrgVariable starts those).
func (c *Core) SetOrgVariable(ctx context.Context, a Actor, key, value string) (apitypes.OrgVariable, error) {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "setting organization variables"); err != nil {
		return apitypes.OrgVariable{}, err
	}
	if err := validKey(key); err != nil {
		return apitypes.OrgVariable{}, err
	}
	sealed, err := c.box.Seal([]byte(value), orgVariableAAD(a.OrgID, key))
	if err != nil {
		return apitypes.OrgVariable{}, err
	}
	n := now()
	if err := c.store.UpsertOrgVariable(ctx, store.UpsertOrgVariableParams{OrgID: a.OrgID, Key: key, Value: sealed, UpdatedAt: n, UpdatedBy: a.Name}); err != nil {
		return apitypes.OrgVariable{}, err
	}
	uses, err := c.orgVariableUses(ctx, a.OrgID)
	if err != nil {
		return apitypes.OrgVariable{}, err
	}
	c.audit(ctx, a, "org.variable_set", nil, nil, key, fmt.Sprintf("set organization variable %s; %d variables use it", key, len(uses[key])))
	out := apitypes.OrgVariable{Key: key, UpdatedAt: n, UpdatedBy: a.Name, UsedBy: uses[key]}
	if out.UsedBy == nil {
		out.UsedBy = []apitypes.VariableUse{}
	}
	return out, nil
}

// DeleteOrgVariable removes an org variable nothing references any more:
// removing one in use would fail those services' next deploy.
func (c *Core) DeleteOrgVariable(ctx context.Context, a Actor, key string) error {
	if err := a.requireUnlimited(apitypes.ScopeAdmin, "removing organization variables"); err != nil {
		return err
	}
	uses, err := c.orgVariableUses(ctx, a.OrgID)
	if err != nil {
		return err
	}
	if u := uses[key]; len(u) > 0 {
		return conflict("%s is used by %s; remove those references first", key, describeUses(u))
	}
	n, err := c.store.DeleteOrgVariable(ctx, store.DeleteOrgVariableParams{OrgID: a.OrgID, Key: key})
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("organization variable %s not found", key)
	}
	c.audit(ctx, a, "org.variable_unset", nil, nil, key, "removed organization variable "+key)
	return nil
}

// describeUses lists uses for a message: "shop/production/web, blog/staging (shared)".
func describeUses(uses []apitypes.VariableUse) string {
	parts := make([]string, len(uses))
	for i, u := range uses {
		parts[i] = u.Project + "/" + u.Environment
		if u.Service != "" {
			parts[i] += "/" + u.Service
		} else {
			parts[i] += " (shared)"
		}
	}
	return strings.Join(parts, ", ")
}

// RedeployOrgVariable starts a config release, with the org variable's
// current value, for every running service that uses it: those whose own
// variables reference it, and every service of an environment whose
// shared variables do. Services the actor can't deploy, without a running
// release or already deploying are skipped with the reason.
func (c *Core) RedeployOrgVariable(ctx context.Context, a Actor, key string) (apitypes.OrgVariableRollout, error) {
	out := apitypes.OrgVariableRollout{Started: []apitypes.RolloutService{}, Skipped: []apitypes.RolloutService{}}
	if err := a.require(apitypes.ScopeDeploy, "redeploying"); err != nil {
		return out, err
	}
	uses, err := c.orgVariableUses(ctx, a.OrgID)
	if err != nil {
		return out, err
	}
	var targets []ServiceRef
	add := func(r ServiceRef) {
		if !slices.Contains(targets, r) {
			targets = append(targets, r)
		}
	}
	for _, u := range uses[key] {
		if u.Service != "" {
			add(ServiceRef{u.Project, u.Environment, u.Service})
			continue
		}
		es, err := c.loadEnv(ctx, a.OrgID, EnvRef{u.Project, u.Environment})
		if err != nil {
			return out, err
		}
		svcs, err := c.store.ListServices(ctx, es.env.ID)
		if err != nil {
			return out, err
		}
		for _, s := range svcs {
			if s.ArchivedAt == nil {
				add(ServiceRef{u.Project, u.Environment, s.Name})
			}
		}
	}
	for _, ref := range targets {
		rs := apitypes.RolloutService{Project: ref.Project, Environment: ref.Environment, Service: ref.Service}
		ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "redeploying")
		if err == nil {
			var rel store.Release
			if rel, err = c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourceConfig}); err == nil {
				c.audit(ctx, a, "release.deploy", &ss.project, &ss.env, ss.String(), fmt.Sprintf("release %d picks up organization variable %s", rel.Number, key))
				rs.Number = rel.Number
				out.Started = append(out.Started, rs)
				continue
			}
		}
		switch {
		case errors.Is(err, errNoActiveRelease):
			rs.Reason = "not running"
		case CodeOf(err) == apitypes.CodeDeployInProgress:
			rs.Reason = "deploying already; it picks the value up if it hasn't resolved its config yet"
		case CodeOf(err) == apitypes.CodeForbidden:
			rs.Reason = "you can't deploy it"
		default:
			if CodeOf(err) == apitypes.CodeInternal {
				return out, err
			}
			rs.Reason = err.Error()
		}
		out.Skipped = append(out.Skipped, rs)
	}
	return out, nil
}
