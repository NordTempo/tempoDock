package core

import (
	"context"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// listServiceAudit returns one service's audit events: those targeting
// project/environment/service. The service doesn't have to exist anymore.
func (c *Core) listServiceAudit(ctx context.Context, a Actor, f AuditFilter) ([]apitypes.AuditEvent, error) {
	if f.Project == "" || f.Environment == "" {
		return nil, invalid("filtering the audit log by service needs its project and environment")
	}
	es, err := c.envFor(ctx, a, EnvRef{f.Project, f.Environment}, apitypes.ScopeRead, "reading the audit log")
	if err != nil {
		return nil, err
	}
	params := store.ListAuditEventsByTargetParams{
		ProjectID: &es.project.ID, EnvironmentID: &es.env.ID, Target: es.env.Name + "/" + f.Service, MaxRows: int64(f.Limit),
	}
	if params.MaxRows <= 0 || params.MaxRows > 500 {
		params.MaxRows = 100
	}
	if f.Action != "" {
		params.ActionFrom, params.ActionTo = prefixRange(f.Action)
	}
	if f.Before > 0 {
		params.BeforeID = &f.Before
	}
	rows, err := c.store.ListAuditEventsByTarget(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.AuditEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, apitypes.AuditEvent{
			ID: r.ID, Actor: r.Actor, ActorKind: r.ActorKind, Action: r.Action, Project: es.project.Name,
			Environment: es.env.Name, Target: r.Target, Summary: r.Summary, DetailID: r.DetailID, CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// prefixRange is the range [from, to) of the actions that start with
// prefix, which queries match instead of `LIKE prefix%` (that makes SQLite
// re-prepare the statement on every call). Actions are lower case, as LIKE
// matched case-insensitively.
func prefixRange(prefix string) (from, to *string) {
	f := strings.ToLower(prefix)
	b := []byte(f)
	b[len(b)-1]++ // never 0xff in UTF-8
	t := string(b)
	return &f, &t
}
