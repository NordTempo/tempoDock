package core

import (
	"context"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/store"
)

// GetAuditDetail returns an audit event's detail by its ID
// (AuditEvent.DetailID), to anyone who may see the event: members of its
// org, limited actors only in their project or environment. The detail of a data.* event (the
// SQL of a query or commit, values included) also needs data access level
// read in its environment, and callers who see sensitive columns masked
// get its values redacted when the database has sensitive columns, unless
// the event is their own.
func (c *Core) GetAuditDetail(ctx context.Context, a Actor, id int64) (apitypes.AuditDetail, error) {
	if err := a.require(apitypes.ScopeRead, "reading the audit log"); err != nil {
		return apitypes.AuditDetail{}, err
	}
	ev, err := c.store.GetAuditEventByDetail(ctx, id)
	if store.IsNotFound(err) {
		return apitypes.AuditDetail{}, notFound("audit detail %d not found", id)
	}
	if err != nil {
		return apitypes.AuditDetail{}, err
	}
	var oid, pid, eid int64
	if ev.OrgID != nil {
		oid = *ev.OrgID
	}
	if ev.ProjectID != nil {
		pid = *ev.ProjectID
	}
	if ev.EnvironmentID != nil {
		eid = *ev.EnvironmentID
	}
	if !a.canSee(oid, pid, eid) || a.limited() && pid == 0 {
		return apitypes.AuditDetail{}, notFound("audit detail %d not found", id)
	}
	var perm apitypes.DataPermission
	own, sensitive := false, false
	if strings.HasPrefix(ev.Action, "data.") {
		if perm, own, sensitive, err = c.dataDetailAccess(ctx, a, ev); err != nil {
			return apitypes.AuditDetail{}, err
		}
	}
	d, err := c.store.GetAuditDetail(ctx, id)
	if err != nil {
		return apitypes.AuditDetail{}, err
	}
	return c.panel.RedactDetail(perm, own, sensitive, toAuditDetail(d)), nil
}

// dataDetailAccess checks that a may read the detail of data event ev
// (level read in its environment) and returns what decides whether its
// values are redacted (datapanel.Panel.RedactDetail): a's permission,
// whether it is their own event and whether the database has sensitive
// columns. The last is only looked up when it matters.
func (c *Core) dataDetailAccess(ctx context.Context, a Actor, ev store.AuditEvent) (perm apitypes.DataPermission, own, sensitive bool, err error) {
	if ev.ProjectID == nil || ev.EnvironmentID == nil {
		return perm, false, false, errf(apitypes.CodeForbidden, "the detail of this %s event isn't tied to an environment", ev.Action)
	}
	env, err := c.store.GetEnvironment(ctx, *ev.EnvironmentID)
	if err != nil {
		return perm, false, false, err
	}
	p, err := c.store.GetProject(ctx, *ev.ProjectID)
	if err != nil {
		return perm, false, false, err
	}
	es := envScope{p, env}
	if perm, err = c.dataPermission(ctx, a, es); err != nil {
		return perm, false, false, err
	}
	if err := datapanel.RequireLevel(perm, apitypes.LevelRead, "reading the SQL of "+ev.Action+" events"); err != nil {
		return perm, false, false, err
	}
	if own = ownEvent(a, ev); perm.Sensitive == apitypes.SensitiveVisible || own {
		return perm, own, false, nil
	}
	name, ok := strings.CutPrefix(ev.Target, es.String()+"/")
	if !ok {
		return perm, false, true, nil
	}
	svc, err := c.store.GetServiceByName(ctx, store.GetServiceByNameParams{EnvironmentID: env.ID, Name: name})
	if store.IsNotFound(err) {
		return perm, false, true, nil // gone: it can't say what's sensitive
	}
	if err != nil {
		return perm, false, false, err
	}
	cols, err := c.store.ListSensitiveColumns(ctx, svc.ID)
	return perm, false, err != nil || len(cols) > 0, err
}

// ownEvent reports whether a recorded ev.
func ownEvent(a Actor, ev store.AuditEvent) bool {
	switch {
	case a.UserID != 0:
		return ev.UserID != nil && *ev.UserID == a.UserID
	case a.TokenID != 0:
		return ev.TokenID != nil && *ev.TokenID == a.TokenID
	}
	return false
}

func toAuditDetail(d store.AuditDetail) apitypes.AuditDetail {
	return apitypes.AuditDetail{ID: d.ID, SQL: d.Sql, Rows: d.Rows, DurationMS: d.DurationMs, Outcome: apitypes.DataOutcome(d.Outcome)}
}
