package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Ignores (ADR 0074, phase 2): an org says a vulnerability doesn't
// matter in a service, a project or the whole org, with a reason, for
// 90 days unless an admin makes it for good. They apply when findings are
// read: an image's stored findings and counts are its own, shared with
// every org that runs it, and each read leaves out what the reader's
// ignores hide where the image runs. An org without ignores pays one
// indexed read.

const (
	// ignoreDays is how long an ignore holds unless it says otherwise.
	ignoreDays = 90
	// keepExpiredIgnores is how long an expired ignore stays listed.
	keepExpiredIgnores = 30 * 24 * time.Hour
)

// ignoreSet is an org's ignores in force, read once per request.
type ignoreSet []store.VulnIgnore

// vulnUse is a place an image runs, as ignores see it.
type vulnUse struct{ project, service int64 }

// ignoresOf reads the org's ignores in force.
func (c *Core) ignoresOf(ctx context.Context, org int64) (ignoreSet, error) {
	return c.store.ListActiveVulnIgnores(ctx, store.ListActiveVulnIgnoresParams{OrgID: org, Now: ptr(now().Unix())})
}

// covers reports whether g applies to vuln in pkg at u.
func covers(g store.VulnIgnore, vuln, pkg string, u vulnUse) bool {
	return g.VulnID == vuln && (g.Package == "" || g.Package == pkg) &&
		(g.ProjectID == 0 || g.ProjectID == u.project) && (g.ServiceID == 0 || g.ServiceID == u.service)
}

// hides reports whether an ignore hides vuln in pkg at u.
func (s ignoreSet) hides(vuln, pkg string, u vulnUse) bool {
	return slices.ContainsFunc(s, func(g store.VulnIgnore) bool { return covers(g, vuln, pkg, u) })
}

// hidden reports whether ignores hide vuln in pkg at every one of uses;
// with no uses, nothing is hidden.
func (s ignoreSet) hidden(vuln, pkg string, uses []vulnUse) bool {
	if len(s) == 0 || len(uses) == 0 {
		return false
	}
	for _, u := range uses {
		if !s.hides(vuln, pkg, u) {
			return false
		}
	}
	return true
}

// vulnIDs are the vulnerabilities the set ignores, each once.
func (s ignoreSet) vulnIDs() []string {
	out := make([]string, 0, len(s))
	for _, g := range s {
		if !slices.Contains(out, g.VulnID) {
			out = append(out, g.VulnID)
		}
	}
	return out
}

// netScan is an image's scan as seen from some of the places it runs.
type netScan struct {
	scan *store.ImageScan
	uses []vulnUse
}

// netScans leaves the findings ignores hide at all of an item's uses out
// of its scan's counts, and works out again what fixes the worst when
// that changed: two queries at most, none without ignores.
func (c *Core) netScans(ctx context.Context, ig ignoreSet, items []netScan) error {
	if len(ig) == 0 {
		return nil
	}
	var ids []int64
	for _, it := range items {
		if it.scan.MatchedAt != nil && !slices.Contains(ids, it.scan.ID) {
			ids = append(ids, it.scan.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := c.store.ListScanFindingsOf(ctx, store.ListScanFindingsOfParams{ScanIds: ids, VulnIds: ig.vulnIDs()})
	if err != nil {
		return err
	}
	byScan := map[int64][]store.ListScanFindingsOfRow{}
	for _, r := range rows {
		byScan[r.ScanID] = append(byScan[r.ScanID], r)
	}
	// What fixes the worst (worst_fix) changes only when the worst
	// severity left changed, or a fixable finding of it was hidden.
	var again []int
	for i, it := range items {
		before := worstLevel(*it.scan)
		var fixHidden [node.SeverityCritical + 1]bool
		for _, f := range byScan[it.scan.ID] {
			if !ig.hidden(f.VulnID, f.Package, it.uses) {
				continue
			}
			uncount(it.scan, f.Severity, f.FixedIn != "", f.Kev != 0)
			if f.FixedIn != "" {
				fixHidden[node.Severity(f.Severity)] = true
			}
		}
		switch after := worstLevel(*it.scan); {
		case after == 0:
			it.scan.WorstFix = ""
		case after != before || fixHidden[after]:
			again = append(again, i)
		}
	}
	if len(again) == 0 {
		return nil
	}
	ids = ids[:0]
	for _, i := range again {
		if !slices.Contains(ids, items[i].scan.ID) {
			ids = append(ids, items[i].scan.ID)
		}
	}
	fixes, err := c.store.ListScanFixesAtLeast(ctx, store.ListScanFixesAtLeastParams{ScanIds: ids, MinSeverity: int64(node.SeverityHigh)})
	if err != nil {
		return err
	}
	for _, i := range again {
		it := items[i]
		level, fix := int64(worstLevel(*it.scan)), apitypes.FixNone
		for _, f := range fixes {
			if f.ScanID != it.scan.ID || f.Severity != level || ig.hidden(f.VulnID, f.Package, it.uses) {
				continue
			}
			if systemPackage(f.PkgType) {
				fix = apitypes.FixRebuild
				break
			}
			fix = apitypes.FixUpgrade
		}
		it.scan.WorstFix = string(fix)
	}
	return nil
}

// worstLevel is the worst severity worst_fix is about: critical, else
// high, else 0.
func worstLevel(s store.ImageScan) node.Severity {
	switch {
	case s.Critical > 0:
		return node.SeverityCritical
	case s.High > 0:
		return node.SeverityHigh
	}
	return 0
}

// uncount takes one finding out of a scan's counts.
func uncount(s *store.ImageScan, severity int64, fixable, kev bool) {
	switch node.Severity(severity) {
	case node.SeverityCritical:
		s.Critical--
	case node.SeverityHigh:
		s.High--
	case node.SeverityMedium:
		s.Medium--
	case node.SeverityLow:
		s.Low--
	default:
		s.Other--
	}
	if fixable {
		s.Fixable--
	}
	if kev {
		s.Kev--
	}
}

// netScan1 is one scan as one service sees it.
func (c *Core) netScan1(ctx context.Context, org int64, s store.ImageScan, u vulnUse) store.ImageScan {
	ig, err := c.ignoresOf(ctx, org)
	if err == nil {
		err = c.netScans(ctx, ig, []netScan{{scan: &s, uses: []vulnUse{u}}})
	}
	if err != nil {
		c.log.Warn("apply vulnerability ignores", "scan", s.ID, "err", err)
	}
	return s
}

// ignoreWhere is where an ignore applies: its project and service IDs
// (0 for broader), and the project, environment and service for the
// audit log and the answer.
type ignoreWhere struct {
	use     vulnUse
	project *store.Project
	env     *store.Environment
	service string
}

// String is the place in words.
func (w ignoreWhere) String() string {
	switch {
	case w.env != nil:
		return serviceKey(w.project.Name, w.env.Name, w.service)
	case w.project != nil:
		return "every service of " + w.project.Name
	}
	return "the whole organization"
}

// ignorePlace finds the place an ignore is asked for, by name.
func (c *Core) ignorePlace(ctx context.Context, a Actor, scope apitypes.IgnoreScope, ref ServiceRef) (ignoreWhere, error) {
	switch scope {
	case apitypes.IgnoreService, "":
		if ref.Project == "" || ref.Environment == "" || ref.Service == "" {
			return ignoreWhere{}, invalid("name the service to ignore it in (project, environment and service), or ignore it for the project or the organization")
		}
		ss, err := c.loadService(ctx, a.OrgID, ref)
		if err != nil {
			return ignoreWhere{}, err
		}
		return ignoreWhere{use: vulnUse{ss.project.ID, ss.svc.ID}, project: &ss.project, env: &ss.env, service: ss.svc.Name}, nil
	case apitypes.IgnoreProject:
		if ref.Project == "" {
			return ignoreWhere{}, invalid("name the project to ignore it in")
		}
		p, err := c.loadProject(ctx, a.OrgID, ref.Project)
		if err != nil {
			return ignoreWhere{}, err
		}
		return ignoreWhere{use: vulnUse{project: p.ID}, project: &p}, nil
	case apitypes.IgnoreOrg:
		return ignoreWhere{}, nil
	}
	return ignoreWhere{}, invalid("scope %q isn't one of service, project, org", scope)
}

// ignorePlaceOf is where a stored ignore applies.
func (c *Core) ignorePlaceOf(ctx context.Context, g store.VulnIgnore) (ignoreWhere, error) {
	switch {
	case g.ServiceID != 0:
		ss, err := c.scopeOf(ctx, g.ServiceID)
		if err != nil {
			return ignoreWhere{}, err
		}
		return ignoreWhere{use: vulnUse{ss.project.ID, ss.svc.ID}, project: &ss.project, env: &ss.env, service: ss.svc.Name}, nil
	case g.ProjectID != 0:
		p, err := c.store.GetProject(ctx, g.ProjectID)
		if err != nil {
			return ignoreWhere{}, err
		}
		return ignoreWhere{use: vulnUse{project: p.ID}, project: &p}, nil
	}
	return ignoreWhere{}, nil
}

// ignoreAccess checks the actor may make or remove an ignore at w:
// deploy on the service's environment (and its lock), deploy on the
// project (and every environment's lock), or admin of the whole org.
func (c *Core) ignoreAccess(ctx context.Context, a Actor, w ignoreWhere, action string) error {
	switch {
	case w.env != nil:
		if err := a.requireScope(apitypes.ScopeDeploy, action); err != nil {
			return err
		}
		return a.checkEnv(apitypes.ScopeDeploy, action, *w.project, *w.env)
	case w.project != nil:
		if err := a.checkProject(apitypes.ScopeDeploy, action, *w.project); err != nil {
			return err
		}
		return c.checkProjectLocks(ctx, a, *w.project, action)
	}
	return a.requireUnlimited(apitypes.ScopeAdmin, action+" in the whole organization")
}

// IgnoreVulnerability ignores a vulnerability where the request says,
// for its days (90 by default) or, for admins, always. Its findings stop
// counting at once: nothing is scanned again, and open alerts about it
// close.
func (c *Core) IgnoreVulnerability(ctx context.Context, a Actor, req apitypes.IgnoreVulnRequest) (apitypes.VulnIgnore, error) {
	const action = "ignoring a vulnerability"
	vuln, pkg, reason := strings.TrimSpace(req.Vulnerability), strings.TrimSpace(req.Package), strings.TrimSpace(req.Reason)
	switch {
	case vuln == "" || strings.ContainsAny(vuln, " \t\n"):
		return apitypes.VulnIgnore{}, invalid("name the vulnerability by its ID, like CVE-2026-22451")
	case reason == "":
		return apitypes.VulnIgnore{}, invalid("say why %s doesn't matter here: the reason is what the next person reads", vuln)
	case req.Always && req.Days != 0:
		return apitypes.VulnIgnore{}, invalid("give days or always, not both")
	case req.Days < 0 || req.Days > 365:
		return apitypes.VulnIgnore{}, invalid("an ignore holds 1 to 365 days; an admin can make one for good with always")
	}
	if err := a.require(apitypes.ScopeRead, action); err != nil {
		return apitypes.VulnIgnore{}, err
	}
	where, err := c.ignorePlace(ctx, a, req.Scope, ServiceRef{req.Project, req.Environment, req.Service})
	if err != nil {
		return apitypes.VulnIgnore{}, err
	}
	if err := c.ignoreAccess(ctx, a, where, action); err != nil {
		return apitypes.VulnIgnore{}, err
	}
	var expires *int64
	until := "always"
	if req.Always {
		if err := a.require(apitypes.ScopeAdmin, "ignoring a vulnerability for good"); err != nil {
			return apitypes.VulnIgnore{}, err
		}
	} else {
		days := cmp.Or(req.Days, ignoreDays)
		at := now().Add(time.Duration(days) * 24 * time.Hour)
		expires, until = ptr(at.Unix()), at.Format("2006-01-02")
	}
	row, err := c.store.UpsertVulnIgnore(ctx, store.UpsertVulnIgnoreParams{
		OrgID: a.OrgID, ProjectID: where.use.project, ServiceID: where.use.service, VulnID: vuln, Package: pkg,
		Reason: reason, ExpiresAt: expires, CreatedBy: a.Name, CreatedAt: now().Unix(),
	})
	if err != nil {
		return apitypes.VulnIgnore{}, err
	}
	if _, err := c.pruneVulnIgnores(ctx); err != nil {
		c.log.Warn("delete expired ignores", "err", err)
	}
	in := ""
	if pkg != "" {
		in = " in " + pkg
	}
	c.audit(ctx, a, "vulns.ignore", where.project, where.env, vuln, fmt.Sprintf("ignored %s%s for %s until %s: %s", vuln, in, where, until, reason))
	c.recheckVulnAlerts(ctx, a.OrgID)
	return where.named(row), nil
}

// ListVulnIgnores is every ignore of the org, newest first, expired ones
// for 30 days: the Ignores page and `dock vulns ignores`. A token limited
// to a project sees the org's and its project's. Scope: read.
func (c *Core) ListVulnIgnores(ctx context.Context, a Actor) (apitypes.VulnIgnores, error) {
	if err := a.require(apitypes.ScopeRead, "reading ignores"); err != nil {
		return apitypes.VulnIgnores{}, err
	}
	rows, err := c.store.ListVulnIgnores(ctx, store.ListVulnIgnoresParams{OrgID: a.OrgID, Cutoff: ptr(now().Add(-keepExpiredIgnores).Unix())})
	if err != nil {
		return apitypes.VulnIgnores{}, err
	}
	out := apitypes.VulnIgnores{Ignores: make([]apitypes.VulnIgnore, 0, len(rows))}
	for _, r := range rows {
		if r.ProjectID != 0 && !a.canSee(a.OrgID, r.ProjectID, r.EnvironmentID) {
			continue
		}
		out.Ignores = append(out.Ignores, toVulnIgnoreRow(r))
	}
	return out, nil
}

// RemoveVulnIgnore stops ignoring: the findings count again. It takes
// what making the ignore took.
func (c *Core) RemoveVulnIgnore(ctx context.Context, a Actor, id int64) error {
	const action = "removing an ignore"
	if err := a.require(apitypes.ScopeRead, action); err != nil {
		return err
	}
	missing := notFound("ignore %d not found; `dock vulns ignores` lists them", id)
	g, err := c.store.GetVulnIgnore(ctx, id)
	if store.IsNotFound(err) || err == nil && g.OrgID != a.OrgID {
		return missing
	}
	if err != nil {
		return err
	}
	where, err := c.ignorePlaceOf(ctx, g)
	if err != nil {
		return err
	}
	if where.project != nil && !a.canSee(a.OrgID, where.project.ID, envID(where.env)) {
		return missing
	}
	if err := c.ignoreAccess(ctx, a, where, action); err != nil {
		return err
	}
	if err := c.store.DeleteVulnIgnore(ctx, id); err != nil {
		return err
	}
	c.audit(ctx, a, "vulns.unignore", where.project, where.env, g.VulnID, fmt.Sprintf("stopped ignoring %s for %s", g.VulnID, where))
	return nil
}

// envID is an environment's ID, 0 for none.
func envID(e *store.Environment) int64 {
	if e == nil {
		return 0
	}
	return e.ID
}

// pruneVulnIgnores deletes ignores expired for 30 days (every org's):
// one indexed delete when an ignore is made, which keeps them off the
// hourly pass. Reads leave them out until then.
func (c *Core) pruneVulnIgnores(ctx context.Context) (int64, error) {
	return c.store.PruneVulnIgnores(ctx, ptr(now().Add(-keepExpiredIgnores).Unix()))
}

// toVulnIgnore is an ignore as the API shows it, named where it applies.
func toVulnIgnore(g store.VulnIgnore, project, env, service string) apitypes.VulnIgnore {
	out := apitypes.VulnIgnore{
		ID: g.ID, Vulnerability: g.VulnID, Package: g.Package, Scope: apitypes.IgnoreOrg, Reason: g.Reason, By: g.CreatedBy,
		Created: time.Unix(g.CreatedAt, 0).UTC(),
	}
	switch {
	case g.ServiceID != 0:
		out.Scope, out.Project, out.Environment, out.Service = apitypes.IgnoreService, project, env, service
	case g.ProjectID != 0:
		out.Scope, out.Project = apitypes.IgnoreProject, project
	}
	if g.ExpiresAt != nil {
		at := time.Unix(*g.ExpiresAt, 0).UTC()
		out.Expires, out.Expired = &at, !at.After(now())
	}
	return out
}

// named is a stored ignore as the API shows it, at w.
func (w ignoreWhere) named(g store.VulnIgnore) apitypes.VulnIgnore {
	var project, env string
	if w.project != nil {
		project = w.project.Name
	}
	if w.env != nil {
		env = w.env.Name
	}
	return toVulnIgnore(g, project, env, w.service)
}

func toVulnIgnoreRow(r store.ListVulnIgnoresRow) apitypes.VulnIgnore {
	return toVulnIgnore(store.VulnIgnore{
		ID: r.ID, OrgID: r.OrgID, ProjectID: r.ProjectID, ServiceID: r.ServiceID, VulnID: r.VulnID, Package: r.Package,
		Reason: r.Reason, ExpiresAt: r.ExpiresAt, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
	}, r.Project, r.Environment, r.Service)
}
