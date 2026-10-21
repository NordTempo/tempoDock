package core

import (
	"context"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Overview returns a project's environments and services with status,
// releases, domains and staged-change counts in one call, for project pages.
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
	servers, err := c.serverNames(ctx)
	if err != nil {
		return out, err
	}
	for _, e := range envs {
		if !a.canSee(p.ID, e.ID) {
			continue
		}
		es := envScope{p, e}
		pending, err := c.pendingChanges(ctx, c.store.Queries, es, false)
		if err != nil {
			return out, err
		}
		eo := apitypes.EnvironmentOverview{
			Environment: toEnvironment(p, e, servers[e.ServerID]), PendingChanges: pending.Count,
			Services: []apitypes.ServiceOverview{},
		}
		svcs, err := c.store.ListServices(ctx, e.ID)
		if err != nil {
			return out, err
		}
		for _, s := range svcs {
			ss := svcScope{es, s}
			svc, err := c.toService(ctx, ss, pending)
			if err != nil {
				return out, err
			}
			so := apitypes.ServiceOverview{Service: svc}
			if r, err := c.store.GetActiveRelease(ctx, s.ID); err == nil {
				so.ActiveRelease = summarize(r)
			} else if !store.IsNotFound(err) {
				return out, err
			}
			if r, err := c.store.GetInFlightRelease(ctx, s.ID); err == nil {
				so.Deploying = summarize(r)
			} else if !store.IsNotFound(err) {
				return out, err
			}
			if so.Domains, err = c.domainsOf(ctx, ss); err != nil {
				return out, err
			}
			eo.Services = append(eo.Services, so)
		}
		out.Environments = append(out.Environments, eo)
	}
	return out, nil
}

// AuditFilter narrows the audit log. Action matches as a prefix
// ("release." matches every release action). Before pages backwards by ID.
// Service needs Project and Environment.
type AuditFilter struct {
	Project, Environment, Service, Action string
	Before                                int64
	Limit                                 int
}

// ListAudit returns audit events, newest first. Actors limited to a
// project or environment only see events there.
func (c *Core) ListAudit(ctx context.Context, a Actor, f AuditFilter) ([]apitypes.AuditEvent, error) {
	if err := a.require(apitypes.ScopeRead, "reading the audit log"); err != nil {
		return nil, err
	}
	if f.Service != "" {
		return c.listServiceAudit(ctx, a, f)
	}
	params := store.ListAuditEventsParams{MaxRows: int64(f.Limit)}
	if params.MaxRows <= 0 || params.MaxRows > 500 {
		params.MaxRows = 100
	}
	switch {
	case f.Environment != "":
		es, err := c.envFor(ctx, a, EnvRef{f.Project, f.Environment}, apitypes.ScopeRead, "reading the audit log")
		if err != nil {
			return nil, err
		}
		params.ProjectID, params.EnvironmentID = &es.project.ID, &es.env.ID
	case f.Project != "":
		p, err := c.projectFor(ctx, a, f.Project, apitypes.ScopeRead, "reading the audit log")
		if err != nil {
			return nil, err
		}
		params.ProjectID = &p.ID
	}
	if a.ProjectID != 0 {
		params.ProjectID = &a.ProjectID
	}
	if a.EnvironmentID != 0 {
		params.EnvironmentID = &a.EnvironmentID
	}
	if f.Action != "" {
		prefix := f.Action + "%"
		params.ActionPrefix = &prefix
	}
	if f.Before > 0 {
		params.BeforeID = &f.Before
	}
	rows, err := c.store.ListAuditEvents(ctx, params)
	if err != nil {
		return nil, err
	}
	names := newNames(c.store)
	out := make([]apitypes.AuditEvent, 0, len(rows))
	for _, r := range rows {
		ae := apitypes.AuditEvent{
			ID: r.ID, Actor: r.Actor, ActorKind: r.ActorKind, Action: r.Action,
			Target: r.Target, Summary: r.Summary, CreatedAt: r.CreatedAt,
		}
		if r.ProjectID != nil {
			ae.Project = names.project(ctx, *r.ProjectID)
		}
		if r.EnvironmentID != nil {
			ae.Environment = names.environment(ctx, *r.EnvironmentID)
		}
		out = append(out, ae)
	}
	return out, nil
}
