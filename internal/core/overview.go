package core

import (
	"context"
	"math"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Overview returns a project's environments and services with status,
// releases, domains and staged-change counts in one call, for project pages.
// It reads the whole project in a few queries (services, release heads,
// volumes, domains, latest backups, uptime, plus staged changes per
// environment), however many services it has.
func (c *Core) Overview(ctx context.Context, a Actor, project string) (apitypes.ProjectOverview, error) {
	p, err := c.projectFor(ctx, a, project, apitypes.ScopeRead, "reading a project")
	if err != nil {
		return apitypes.ProjectOverview{}, err
	}
	out := apitypes.ProjectOverview{Project: toProject(p), Environments: []apitypes.EnvironmentOverview{}}
	envs, err := c.store.ListEnvironments(ctx, p.ID)
	if err != nil {
		return out, err
	}
	servers, err := c.serverRows(ctx)
	if err != nil {
		return out, err
	}
	prs, err := c.previewEnvironments(ctx, p.ID)
	if err != nil {
		return out, err
	}
	svcs, err := c.store.ListProjectServices(ctx, p.ID)
	if err != nil {
		return out, err
	}
	diags, err := c.store.ListProjectDiagnosisHeads(ctx, p.ID)
	if err != nil {
		return out, err
	}
	diagOf := make(map[int64]*apitypes.DiagnosisRef, len(diags)) // by release ID
	for _, d := range diags {
		diagOf[d.ReleaseID] = &apitypes.DiagnosisRef{ID: d.ID, Kind: apitypes.DiagnosisKind(d.Kind), Rule: d.Rule, Summary: d.Summary}
	}
	heads, err := c.store.ListProjectReleaseHeads(ctx, p.ID)
	if err != nil {
		return out, err
	}
	// The live images' scans (ADR 0074), for the overview's strip.
	scanRows, err := c.store.ListProjectImageScans(ctx, p.ID)
	if err != nil {
		return out, err
	}
	scanOf := make(map[int64]store.ImageScan, len(scanRows)) // by service ID
	for _, r := range scanRows {
		scanOf[r.ServiceID] = store.ImageScan{
			ID: r.ID, ImageKey: r.ImageKey, LocalRef: r.LocalRef, State: r.State, Distro: r.Distro, Packages: r.Packages,
			MatchedAt: r.MatchedAt, DbBuiltAt: r.DbBuiltAt, Critical: r.Critical, High: r.High, Medium: r.Medium,
			Low: r.Low, Other: r.Other, Fixable: r.Fixable, Kev: r.Kev, Truncated: r.Truncated, WorstFix: r.WorstFix, Error: r.Error,
		}
	}
	vols, err := c.store.ListProjectVolumes(ctx, p.ID)
	if err != nil {
		return out, err
	}
	domains, err := c.store.ListProjectDomains(ctx, p.ID)
	if err != nil {
		return out, err
	}
	backups, err := c.latestBackups(ctx, p.ID)
	if err != nil {
		return out, err
	}
	uptimes, err := c.serviceUptimes(ctx, p.ID)
	if err != nil {
		return out, err
	}

	svcsByEnv := map[int64][]store.Service{}
	for _, s := range svcs {
		svcsByEnv[s.EnvironmentID] = append(svcsByEnv[s.EnvironmentID], s)
	}
	releases := releaseHeadsByService(heads)
	stateful := map[int64]bool{}
	for _, v := range vols {
		stateful[v.ServiceID] = stateful[v.ServiceID] || !v.Pending
	}
	domainsBySvc := map[int64][]store.Domain{}
	for _, d := range domains {
		domainsBySvc[d.ServiceID] = append(domainsBySvc[d.ServiceID], d)
	}

	// Running moves, for every environment at once (ADR 0053).
	moves, err := c.store.ListOpenEnvironmentMoves(ctx)
	if err != nil {
		return out, err
	}
	movingTo := map[int64]int64{}
	for _, m := range moves {
		movingTo[m.EnvironmentID] = m.ToServerID
	}

	for _, e := range envs {
		if !a.canSee(p.OrgID, p.ID, e.ID) {
			continue
		}
		es := envScope{p, e}
		srv := servers[e.ServerID]
		ing := c.ingressOf(srv)
		link := serverLink{name: a.serverName(srv.Name), status: c.serverStatus(srv)}
		pending, _, _, err := c.stagedChanges(ctx, c.store.Queries, es, false)
		if err != nil {
			return out, err
		}
		env := c.toEnvironment(a, p, e, srv)
		env.Preview = prs[e.ID]
		if to, ok := movingTo[e.ID]; ok {
			env.Moving, env.MovingTo = true, a.serverName(servers[to].Name)
		}
		eo := apitypes.EnvironmentOverview{
			Environment: env, PendingChanges: pending.Count,
			Services: make([]apitypes.ServiceOverview, 0, len(svcsByEnv[e.ID])),
		}
		for _, s := range svcsByEnv[e.ID] {
			ss := svcScope{es, s}
			rh := releases[s.ID]
			var active *int64
			so := apitypes.ServiceOverview{Domains: make([]apitypes.Domain, 0, len(domainsBySvc[s.ID]))}
			// Each with its service's newest diagnosis, when it is that
			// release's (ADR 0071).
			withDiag := func(r *store.Release) *apitypes.ReleaseSummary {
				out := summarize(*r)
				out.Diagnosis = diagOf[r.ID]
				return out
			}
			if rh.active != nil {
				active = &rh.active.Number
				so.ActiveRelease = withDiag(rh.active)
			}
			if rh.deploying != nil {
				so.Deploying = summarize(*rh.deploying)
			}
			if rh.latest != nil {
				so.LatestRelease = withDiag(rh.latest)
			}
			if rh.lastFailed != nil {
				so.LastFailedDeploy = withDiag(rh.lastFailed)
			}
			so.Service = c.serviceOf(ss, link, pending, active, stateful[s.ID])
			if sc, ok := scanOf[s.ID]; ok && rh.active != nil {
				scan := toImageScan(sc, c.imageName(rh.active.ImageRef, apitypes.ImageUse{Project: p.Name, Service: s.Name, Release: rh.active.Number}))
				so.Scan = &scan
			}
			if b, ok := backups[s.ID]; ok {
				run := toBackupRun(b, s.Name, nil)
				so.Backup = &run
			}
			for _, d := range domainsBySvc[s.ID] {
				so.Domains = append(so.Domains, c.toDomain(ss, d, domainsBySvc[s.ID], ing))
			}
			so.Uptime = uptimes[s.ID]
			eo.Services = append(eo.Services, so)
		}
		out.Environments = append(out.Environments, eo)
	}
	return out, nil
}

// releaseHeads are the releases an overview shows for one service;
// lastFailed is its newest finished deploy when that failed.
type releaseHeads struct {
	active, deploying, latest, lastFailed *store.Release
}

// releaseHeadsByService sorts ListProjectReleaseHeads rows by service and role.
func releaseHeadsByService(rows []store.Release) map[int64]releaseHeads {
	out := map[int64]releaseHeads{}
	for i := range rows {
		r := &rows[i]
		h := out[r.ServiceID]
		switch status := apitypes.ReleaseStatus(r.Status); {
		case status == apitypes.ReleaseActive:
			h.active = r
		case !status.Done():
			h.deploying = r
		case status == apitypes.ReleaseFailed && r.Source != string(apitypes.SourceRollback) && r.Source != string(apitypes.SourceRestart):
			// Only the query's last-deploy row is a failed deploy that
			// isn't the latest release; a failed latest one is both.
			h.lastFailed = r
		}
		if h.latest == nil || r.Number > h.latest.Number {
			h.latest = r
		}
		out[r.ServiceID] = h
	}
	return out
}

// AuditFilter narrows the audit log. Action matches as a prefix
// ("release." matches every release action). Before pages backwards by ID.
// Service needs Project and Environment.
type AuditFilter struct {
	Project, Environment, Service, Action string
	Before                                int64
	Limit                                 int
	// Platform lists the platform's events (those in no org) instead of
	// the actor's org's; operators only.
	Platform bool
}

// ListAudit returns the actor's org's audit events, newest first, or with
// f.Platform the platform's. Actors limited to a project or environment
// only see events there.
func (c *Core) ListAudit(ctx context.Context, a Actor, f AuditFilter) ([]apitypes.AuditEvent, error) {
	if f.Platform {
		if err := a.requireOperator("reading the platform's audit log"); err != nil {
			return nil, err
		}
		if f.Project != "" || f.Service != "" {
			return nil, invalid("the platform's audit log has no projects; drop the project filter")
		}
	} else if err := a.require(apitypes.ScopeRead, "reading the audit log"); err != nil {
		return nil, err
	}
	if f.Service != "" {
		return c.listServiceAudit(ctx, a, f)
	}
	params := store.ListAuditEventsParams{MaxRows: int64(f.Limit), BeforeID: f.Before}
	if !f.Platform {
		params.OrgID = &a.OrgID
	}
	if params.MaxRows <= 0 || params.MaxRows > 500 {
		params.MaxRows = 100
	}
	if params.BeforeID <= 0 {
		params.BeforeID = math.MaxInt64
	}
	var projectID *int64
	switch {
	case f.Environment != "":
		es, err := c.envFor(ctx, a, EnvRef{f.Project, f.Environment}, apitypes.ScopeRead, "reading the audit log")
		if err != nil {
			return nil, err
		}
		projectID, params.EnvironmentID = &es.project.ID, &es.env.ID
	case f.Project != "":
		p, err := c.projectFor(ctx, a, f.Project, apitypes.ScopeRead, "reading the audit log")
		if err != nil {
			return nil, err
		}
		projectID = &p.ID
	}
	if a.ProjectID != 0 {
		projectID = &a.ProjectID
	}
	if a.EnvironmentID != 0 {
		params.EnvironmentID = &a.EnvironmentID
	}
	if f.Action != "" {
		params.ActionFrom, params.ActionTo = prefixRange(f.Action)
	}
	var rows []store.ListAuditEventsRow
	switch {
	case projectID == nil && params.EnvironmentID == nil && params.ActionFrom != nil:
		// Through audit_events_org_action, however rare the action.
		arows, err := c.store.ListAuditEventsByAction(ctx, store.ListAuditEventsByActionParams{
			OrgID: params.OrgID, ActionFrom: *params.ActionFrom, ActionTo: *params.ActionTo, BeforeID: params.BeforeID, MaxRows: params.MaxRows,
		})
		if err != nil {
			return nil, err
		}
		rows = make([]store.ListAuditEventsRow, len(arows))
		for i, r := range arows {
			rows[i] = store.ListAuditEventsRow(r)
		}
	case projectID != nil && params.ActionFrom != nil:
		prows, err := c.store.ListProjectAuditEventsByAction(ctx, store.ListProjectAuditEventsByActionParams{
			ProjectID: projectID, ActionFrom: *params.ActionFrom, ActionTo: *params.ActionTo,
			BeforeID: params.BeforeID, EnvironmentID: params.EnvironmentID, MaxRows: params.MaxRows,
		})
		if err != nil {
			return nil, err
		}
		rows = make([]store.ListAuditEventsRow, len(prows))
		for i, r := range prows {
			rows[i] = store.ListAuditEventsRow(r)
		}
	case projectID != nil:
		// A separate query, so SQLite reads the project's events through
		// audit_events_project instead of scanning the whole log.
		prows, err := c.store.ListProjectAuditEvents(ctx, store.ListProjectAuditEventsParams{
			ProjectID: projectID, BeforeID: params.BeforeID, EnvironmentID: params.EnvironmentID,
			ActionFrom: params.ActionFrom, ActionTo: params.ActionTo, MaxRows: params.MaxRows,
		})
		if err != nil {
			return nil, err
		}
		rows = make([]store.ListAuditEventsRow, len(prows))
		for i, r := range prows {
			rows[i] = store.ListAuditEventsRow(r)
		}
	default:
		var err error
		if rows, err = c.store.ListAuditEvents(ctx, params); err != nil {
			return nil, err
		}
	}
	out := make([]apitypes.AuditEvent, 0, len(rows))
	for _, r := range rows {
		e := r.AuditEvent
		out = append(out, apitypes.AuditEvent{
			ID: e.ID, Actor: e.Actor, ActorKind: e.ActorKind, Action: e.Action,
			Project: r.ProjectName, Environment: r.EnvironmentName,
			Target: e.Target, Summary: e.Summary, DetailID: e.DetailID, CreatedAt: e.CreatedAt,
		})
	}
	return out, nil
}
