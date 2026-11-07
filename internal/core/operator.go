package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Operators administer every org without being members (ADR 0062). They
// see each org's size, use and people, and may change its settings and
// members, suspend it, stop its services and delete it, each audited in
// the platform's log and the org's. Nothing here reads an org's content:
// variables, logs, data, code. For that an operator joins the org for a
// while (JoinOrgAsOperator), which its admins see in their log.

// operatorIn is an operator acting on org for one of the console's org
// actions: an unlimited admin there. Only the functions in this file make
// one and hand it to the org's own operations (renaming, members), so it
// never reaches the org's content.
func (a Actor) operatorIn(o store.Org) Actor {
	a.OrgID, a.OrgSlug, a.orgs, a.Scope = o.ID, o.Slug, 1, apitypes.ScopeAdmin
	a.ProjectID, a.EnvironmentID, a.ProjectName, a.EnvironmentName = 0, 0, "", ""
	a.suspended = false
	return a
}

// operatorOrg checks a is an operator and loads the org named by slug.
func (c *Core) operatorOrg(ctx context.Context, a Actor, slug, action string) (store.Org, error) {
	if err := a.requireOperator(action); err != nil {
		return store.Org{}, err
	}
	o, err := c.orgBySlug(ctx, slug)
	if store.IsNotFound(err) || err == nil && o.DeletedAt != nil {
		return store.Org{}, notFound("organization %q not found; the console's Organizations page lists them", slug)
	}
	return o, err
}

// auditBoth records an operator's action on an org twice: in the org's
// log, where its admins see it, and in the platform's.
func (c *Core) auditBoth(ctx context.Context, a Actor, o store.Org, action, summary string) {
	c.audit(ctx, a.operatorIn(o), action, nil, nil, o.Slug, summary)
	c.audit(ctx, a.platform(), action, nil, nil, o.Slug, summary+" (organization "+o.Slug+")")
}

// OperatorOrgs lists every org with its people, size, status and use,
// biggest CPU first, in a fixed number of queries however many orgs:
// orgs with their counts, their admins, the services' orgs, CPU a week
// ago, backups, traffic and the registry's images. CPU and memory now
// come from the statistics in memory, volumes from the servers' last
// disk readings. With readDisks, a server never read, or read over a
// minute ago, is read again in the background, as opening its disk page
// does, and the result arrives as a disk event. Operators only.
func (c *Core) OperatorOrgs(ctx context.Context, a Actor, readDisks bool) ([]apitypes.OperatorOrg, error) {
	if err := a.requireOperator("listing organizations"); err != nil {
		return nil, err
	}
	orgs, err := c.store.ListOperatorOrgs(ctx)
	if err != nil {
		return nil, err
	}
	admins, err := c.store.ListOrgAdmins(ctx)
	if err != nil {
		return nil, err
	}
	use, err := c.orgUsage(ctx)
	if err != nil {
		return nil, err
	}
	if readDisks {
		c.refreshStaleDisks(ctx)
	}
	byOrg := map[int64][]string{}
	for _, r := range admins {
		byOrg[r.OrgID] = append(byOrg[r.OrgID], r.Email)
	}
	out := make([]apitypes.OperatorOrg, 0, len(orgs))
	for _, r := range orgs {
		o := apitypes.OperatorOrg{
			Slug: r.Org.Slug, Name: r.Org.Name, CreatedAt: r.Org.CreatedAt, Status: apitypes.OrgActive,
			Admins: byOrg[r.Org.ID], Members: int(r.Members), Projects: int(r.Projects), Services: int(r.Services),
			Usage: use[r.Org.ID],
		}
		if len(o.Admins) > 0 {
			o.Owner = o.Admins[0]
		} else {
			o.Admins, o.Status = []string{}, apitypes.OrgNoAdmin
		}
		if r.Org.SuspendedAt != nil {
			o.Status, o.SuspendedAt, o.SuspendedBy = apitypes.OrgSuspended, r.Org.SuspendedAt, r.Org.SuspendedBy
		}
		out = append(out, o)
	}
	slices.SortStableFunc(out, func(x, y apitypes.OperatorOrg) int {
		return cmp.Or(cmp.Compare(y.Usage.CPUPercent, x.Usage.CPUPercent), cmp.Compare(y.Usage.MemoryBytes, x.Usage.MemoryBytes))
	})
	return out, nil
}

// orgUsage adds up each org's use (see OperatorOrgs).
func (c *Core) orgUsage(ctx context.Context) (map[int64]apitypes.OrgUsage, error) {
	svcs, err := c.store.ListServiceOrgs(ctx)
	if err != nil {
		return nil, err
	}
	t := now()
	weekAgo := t.Add(-7 * 24 * time.Hour).Unix()
	before, err := c.store.ListServiceCPUBetween(ctx, store.ListServiceCPUBetweenParams{FromAt: weekAgo, ToAt: weekAgo + 3600})
	if err != nil {
		return nil, err
	}
	backups, err := c.store.SumOrgBackups(ctx)
	if err != nil {
		return nil, err
	}
	traffic, err := c.store.SumOrgTraffic(ctx, t.Add(-30*24*time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	images, err := c.store.ListRegistryServices(ctx, int64(c.keepReleases()))
	if err != nil {
		return nil, err
	}

	use := map[int64]apitypes.OrgUsage{}
	add := func(org int64, f func(u *apitypes.OrgUsage)) {
		u := use[org]
		f(&u)
		use[org] = u
	}
	orgOf := make(map[int64]int64, len(svcs))
	projectOrg := map[int64]int64{}
	for _, s := range svcs {
		orgOf[s.ServiceID], projectOrg[s.ProjectID] = s.OrgID, s.OrgID
	}
	c.stats.mu.Lock()
	for id, u := range c.stats.service {
		if org, ok := orgOf[id]; ok {
			add(org, func(o *apitypes.OrgUsage) { o.CPUPercent += u.CPUPercent; o.MemoryBytes += u.MemoryBytes })
		}
	}
	c.stats.mu.Unlock()
	for _, r := range before {
		if org, ok := orgOf[r.ServiceID]; ok {
			add(org, func(o *apitypes.OrgUsage) { o.CPUWeekAgoPercent += float64(r.Cpu) / 100 })
		}
	}
	for _, r := range backups {
		add(r.OrgID, func(o *apitypes.OrgUsage) { o.BackupBytes += r.Bytes })
	}
	for _, r := range traffic {
		add(r.OrgID, func(o *apitypes.OrgUsage) { o.BandwidthBytes += r.Bytes })
	}
	for _, r := range images {
		if org, ok := projectOrg[r.ProjectID]; ok && r.Kept > 0 {
			add(org, func(o *apitypes.OrgUsage) { o.Images += int(r.Kept) })
		}
	}
	c.disk.mu.Lock()
	for srv, vols := range c.disk.volumes {
		at := c.disk.last[srv].ReadAt
		for id, b := range vols {
			if org, ok := orgOf[id]; ok {
				add(org, func(o *apitypes.OrgUsage) {
					o.VolumeBytes += b
					if o.VolumeReadAt == nil || at.Before(*o.VolumeReadAt) {
						o.VolumeReadAt = &at
					}
				})
			}
		}
	}
	c.disk.mu.Unlock()
	return use, nil
}

// refreshStaleDisks reads again, in the background, each online server's
// disk that was never read or was read over diskFresh ago, at most once
// per diskFresh per server.
func (c *Core) refreshStaleDisks(ctx context.Context) {
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return
	}
	t := time.Now()
	for _, srv := range servers {
		s := &c.disk
		s.mu.Lock()
		d, ok := s.last[srv.ID]
		stale := (!ok || t.Sub(d.ReadAt) > diskFresh) && t.Sub(s.tried[srv.ID]) > diskFresh
		if stale {
			if s.tried == nil {
				s.tried = map[int64]time.Time{}
			}
			s.tried[srv.ID] = t
		}
		s.mu.Unlock()
		if stale && c.connected(srv.ID) != nil {
			c.refreshDisk(srv)
		}
	}
}

// OperatorRenameOrg changes any org's display name or slug, as its
// admins do (RenameOrg), also audited in the platform's log. Operators
// only.
func (c *Core) OperatorRenameOrg(ctx context.Context, a Actor, slug string, req apitypes.UpdateOrgRequest) (apitypes.Org, error) {
	o, err := c.operatorOrg(ctx, a, slug, "renaming organizations")
	if err != nil {
		return apitypes.Org{}, err
	}
	out, err := c.RenameOrg(ctx, a.operatorIn(o), req)
	if err != nil {
		return out, err
	}
	if out.Name != o.Name {
		c.audit(ctx, a.platform(), "org.rename", nil, nil, out.Slug, fmt.Sprintf("renamed organization %s to %s", out.Slug, out.Name))
	}
	if out.Slug != o.Slug {
		c.audit(ctx, a.platform(), "org.slug", nil, nil, out.Slug, fmt.Sprintf("moved organization %s to /%s; /%s keeps working", o.Slug, out.Slug, o.Slug))
	}
	return out, nil
}

// OperatorMembers lists any org's members, with their roles. Operators
// only.
func (c *Core) OperatorMembers(ctx context.Context, a Actor, slug string) ([]apitypes.User, error) {
	o, err := c.operatorOrg(ctx, a, slug, "managing organizations' members")
	if err != nil {
		return nil, err
	}
	return c.ListUsers(ctx, a.operatorIn(o))
}

// OperatorAddMember adds an existing user to an org with a role: how an
// org without an admin gets one. Operators only.
func (c *Core) OperatorAddMember(ctx context.Context, a Actor, slug string, req apitypes.OperatorMemberRequest) (apitypes.User, error) {
	o, err := c.operatorOrg(ctx, a, slug, "managing organizations' members")
	if err != nil {
		return apitypes.User{}, err
	}
	if !validScope(req.Role) {
		return apitypes.User{}, invalid("role must be read, deploy or admin")
	}
	u, err := c.store.GetUserByEmail(ctx, strings.TrimSpace(req.Email))
	if store.IsNotFound(err) {
		return apitypes.User{}, notFound("no user has email %q; create the account in the console's Users, or invite them", req.Email)
	}
	if err != nil {
		return apitypes.User{}, err
	}
	if _, err := c.store.GetMemberRole(ctx, store.GetMemberRoleParams{OrgID: o.ID, UserID: u.ID}); err == nil {
		return apitypes.User{}, conflict("%s is already a member of %s; change their role instead", u.Email, o.Slug)
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		// A lapsed operator membership may still be there until retention.
		if err := q.DeleteOrgMember(ctx, store.DeleteOrgMemberParams{OrgID: o.ID, UserID: u.ID}); err != nil {
			return err
		}
		return q.AddOrgMember(ctx, store.AddOrgMemberParams{OrgID: o.ID, UserID: u.ID, Role: string(req.Role)})
	})
	if err != nil {
		return apitypes.User{}, err
	}
	c.auditBoth(ctx, a, o, "org.member_add", fmt.Sprintf("the platform's operator added %s as %s", u.Email, req.Role))
	return toUser(u, string(req.Role)), nil
}

// OperatorSetMemberRole changes a member's role in any org, keeping it an
// admin of its own. Operators only.
func (c *Core) OperatorSetMemberRole(ctx context.Context, a Actor, slug string, userID int64, req apitypes.OperatorMemberRequest) (apitypes.User, error) {
	o, err := c.operatorOrg(ctx, a, slug, "managing organizations' members")
	if err != nil {
		return apitypes.User{}, err
	}
	out, err := c.SetMemberRole(ctx, a.operatorIn(o), userID, apitypes.SetMemberRoleRequest{Role: req.Role})
	if err == nil {
		c.audit(ctx, a.platform(), "org.member_role", nil, nil, o.Slug, fmt.Sprintf("made %s %s in organization %s", out.Email, req.Role, o.Slug))
	}
	return out, err
}

// OperatorRemoveMember takes a member out of any org, keeping it an admin
// of its own. Operators only.
func (c *Core) OperatorRemoveMember(ctx context.Context, a Actor, slug string, userID int64) error {
	o, err := c.operatorOrg(ctx, a, slug, "managing organizations' members")
	if err != nil {
		return err
	}
	if err := c.keepAnAdmin(ctx, o.ID, userID, ""); err != nil {
		return err
	}
	op := a.operatorIn(o)
	op.UserID = 0 // RemoveMember refuses to remove "yourself"; the operator leaves with LeaveOrg
	u, err := c.member(ctx, op, userID, "removing members")
	if err != nil {
		return err
	}
	if _, err := c.store.RemoveOrgMember(ctx, store.RemoveOrgMemberParams{OrgID: o.ID, UserID: userID}); err != nil {
		return err
	}
	c.auditBoth(ctx, a, o, "org.member_remove", "the platform's operator removed "+u.Email+" from the organization")
	return nil
}

// SuspendOrg suspends an org (suspend) or lifts it: while suspended its
// apps keep running, but every deploy and change in it is refused, its
// members and tokens may only read. Operators only.
func (c *Core) SuspendOrg(ctx context.Context, a Actor, slug string, suspend bool) (apitypes.OperatorOrg, error) {
	o, err := c.operatorOrg(ctx, a, slug, "suspending organizations")
	if err != nil {
		return apitypes.OperatorOrg{}, err
	}
	params := store.SuspendOrgParams{ID: o.ID}
	action, summary := "org.unsuspend", "the platform's operator lifted the suspension: deploys and changes work again"
	if suspend {
		params.SuspendedAt, params.SuspendedBy = ptr(now()), a.Name
		action, summary = "org.suspend", "the platform's operator suspended the organization: its apps keep running, deploys and changes are refused"
	}
	if (o.SuspendedAt != nil) != suspend {
		if o, err = c.store.SuspendOrg(ctx, params); err != nil {
			return apitypes.OperatorOrg{}, err
		}
		c.auditBoth(ctx, a, o, action, summary)
	}
	return c.operatorOrgView(ctx, a, o.Slug)
}

// operatorOrgView is one org as OperatorOrgs lists it.
func (c *Core) operatorOrgView(ctx context.Context, a Actor, slug string) (apitypes.OperatorOrg, error) {
	all, err := c.OperatorOrgs(ctx, a, false)
	if err != nil {
		return apitypes.OperatorOrg{}, err
	}
	for _, o := range all {
		if o.Slug == slug {
			return o, nil
		}
	}
	return apitypes.OperatorOrg{}, notFound("organization %q not found", slug)
}

// StopOrgServices stops every running service of an org, as `dock stop`
// does each: their domains show the 503 page, volumes and history stay,
// and starting or deploying one runs it again. Services that don't stop
// (deploying, or on a server that can't be reached) are listed. Operators
// only.
func (c *Core) StopOrgServices(ctx context.Context, a Actor, slug string) (apitypes.StoppedOrgServices, error) {
	o, err := c.operatorOrg(ctx, a, slug, "stopping organizations' services")
	if err != nil {
		return apitypes.StoppedOrgServices{}, err
	}
	svcs, err := c.store.ListOrgLiveServices(ctx, o.ID)
	if err != nil {
		return apitypes.StoppedOrgServices{}, err
	}
	var out apitypes.StoppedOrgServices
	sys := System.InOrg(o.ID)
	sys.Name = a.Name
	for _, s := range svcs {
		if s.StoppedAt != nil {
			continue
		}
		ref := ServiceRef{Project: s.Project, Environment: s.Environment, Service: s.Service}
		if _, err := c.StopService(ctx, sys, ref); err != nil {
			out.Failed = append(out.Failed, serviceKey(s.Project, s.Environment, s.Service)+": "+err.Error())
			continue
		}
		out.Stopped++
	}
	c.auditBoth(ctx, a, o, "org.stop_services", fmt.Sprintf("the platform's operator stopped %d services", out.Stopped))
	return out, nil
}

// OperatorDeleteOrg deletes an org; confirm must be its slug. An org
// without projects goes as DeleteOrg deletes it. One with projects keeps
// their history: every project is archived (containers stopped, routes
// removed, volumes and releases kept), its members, invites, variables,
// credentials and policy removed, its tokens revoked, and the org itself
// stays hidden from every list. Operators only.
func (c *Core) OperatorDeleteOrg(ctx context.Context, a Actor, slug, confirm string) error {
	o, err := c.operatorOrg(ctx, a, slug, "deleting organizations")
	if err != nil {
		return err
	}
	if confirm != o.Slug {
		return invalid("type the organization's slug, %s, to confirm deleting it", o.Slug)
	}
	n, err := c.store.CountOrgProjects(ctx, o.ID)
	if err != nil {
		return err
	}
	if n == 0 {
		if err := c.DeleteOrg(ctx, a.operatorIn(o)); err != nil {
			return err
		}
		c.audit(ctx, a.platform(), "org.delete", nil, nil, o.Slug, "the platform's operator deleted organization "+o.Slug)
		return nil
	}
	projects, err := c.store.ListProjects(ctx, o.ID)
	if err != nil {
		return err
	}
	sys := System.InOrg(o.ID)
	sys.Name = a.Name
	for _, p := range projects {
		if p.ArchivedAt != nil {
			continue
		}
		if _, err := c.ArchiveProject(ctx, sys, p.Name); err != nil {
			return fmt.Errorf("archive project %s first: %w", p.Name, err)
		}
	}
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		for _, del := range []func(context.Context, int64) error{q.DeleteOrgMembers, q.DeleteOrgInvites, q.DeleteOrgVariables, q.DeleteOrgRegistryCredentials, q.DeleteOrgDataPolicy} {
			if err := del(ctx, o.ID); err != nil {
				return err
			}
		}
		if err := q.RevokeOrgTokens(ctx, store.RevokeOrgTokensParams{Now: ptr(now()), OrgID: &o.ID}); err != nil {
			return err
		}
		return q.MarkOrgDeleted(ctx, store.MarkOrgDeletedParams{DeletedAt: ptr(now()), ID: o.ID})
	})
	if err != nil {
		return err
	}
	c.auditBoth(ctx, a, o, "org.delete", fmt.Sprintf("the platform's operator deleted the organization: its %d projects are archived and kept for their history", len(projects)))
	return nil
}

// maxJoinHours bounds Join as operator.
const maxJoinHours = 24

// JoinOrgAsOperator makes the operator an admin of the org for hours (1
// when 0, at most maxJoinHours): the audited way to see an org's content
// when that is needed. The membership ends by itself: lookups ignore it
// after expires_at, and the retention pass removes it. An operator who is
// a member already doesn't need it. People only, not tokens.
func (c *Core) JoinOrgAsOperator(ctx context.Context, a Actor, slug string, req apitypes.JoinOrgRequest) (apitypes.OrgMembershipGrant, error) {
	o, err := c.operatorOrg(ctx, a, slug, "joining organizations as operator")
	if err != nil {
		return apitypes.OrgMembershipGrant{}, err
	}
	if a.UserID == 0 {
		return apitypes.OrgMembershipGrant{}, errf(apitypes.CodeForbidden, "joining an organization is for a person, not %s; sign in to the console", a)
	}
	hours := cmp.Or(req.Hours, 1)
	if hours < 1 || hours > maxJoinHours {
		return apitypes.OrgMembershipGrant{}, invalid("hours must be 1 to %d", maxJoinHours)
	}
	if _, err := c.store.GetMemberRole(ctx, store.GetMemberRoleParams{OrgID: o.ID, UserID: a.UserID}); err == nil {
		return apitypes.OrgMembershipGrant{}, conflict("you are a member of %s already", o.Slug)
	}
	until := now().Add(time.Duration(hours) * time.Hour).Truncate(time.Second)
	err = c.store.Tx(ctx, func(q *store.Queries) error {
		if err := q.DeleteOrgMember(ctx, store.DeleteOrgMemberParams{OrgID: o.ID, UserID: a.UserID}); err != nil {
			return err // a lapsed one, not removed yet
		}
		return q.AddExpiringOrgMember(ctx, store.AddExpiringOrgMemberParams{OrgID: o.ID, UserID: a.UserID, ExpiresAt: ptr(until.Unix())})
	})
	if err != nil {
		return apitypes.OrgMembershipGrant{}, err
	}
	c.auditBoth(ctx, a, o, "org.operator_join", fmt.Sprintf("the platform's operator %s joined as an admin until %s", a.Email, until.UTC().Format("2006-01-02 15:04 MST")))
	return apitypes.OrgMembershipGrant{Org: o.Slug, Role: apitypes.ScopeAdmin, ExpiresAt: until.UTC()}, nil
}

// expireOperatorMembers removes operators' memberships that ended and
// audits each in its org's log. The retention pass runs it; access ended
// at expires_at already, as lookups ignore expired memberships.
func (c *Core) expireOperatorMembers(ctx context.Context) {
	rows, err := c.store.ListExpiredOrgMembers(ctx)
	if err != nil || len(rows) == 0 {
		return
	}
	for _, r := range rows {
		if err := c.store.DeleteOrgMember(ctx, store.DeleteOrgMemberParams{OrgID: r.OrgID, UserID: r.UserID}); err != nil {
			c.log.Error("expire an operator's membership", "org", r.OrgID, "err", err)
			continue
		}
		c.audit(ctx, System.InOrg(r.OrgID), "org.operator_leave", nil, nil, r.Email, "the platform's operator "+r.Email+" is no longer a member: their time ran out")
	}
}
