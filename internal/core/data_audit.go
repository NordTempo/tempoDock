package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/store"
)

// browseInterval is how often one actor's browsing of one table is
// audited (data.browse): the first page, then at most once per interval.
// Reveals (data.reveal) are throttled the same way.
const browseInterval = 10 * time.Minute

// browseLogMax bounds the browse throttle's memory: past it, entries older
// than browseInterval are dropped when the next one is added.
const browseLogMax = 1024

// browseLog remembers when each actor last had a data.browse or
// data.reveal event per table, in memory only: after a restart the next
// page is audited again. No timer: old entries are swept when the map
// grows.
type browseLog struct {
	mu   sync.Mutex
	last map[browseKey]time.Time
}

type browseKey struct {
	// action is the audit action, AuditDataBrowse or AuditDataReveal.
	action  string
	actor   string
	service int64
	table   string
}

// due reports whether a browse should be audited now, and if so records it.
func (b *browseLog) due(k browseKey, at time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.last[k]; ok && at.Sub(t) < browseInterval {
		return false
	}
	if b.last == nil {
		b.last = map[browseKey]time.Time{}
	}
	if len(b.last) >= browseLogMax {
		for key, t := range b.last {
			if at.Sub(t) >= browseInterval {
				delete(b.last, key)
			}
		}
	}
	b.last[k] = at
	return true
}

// actorKey identifies an actor across requests.
func actorKey(a Actor) string {
	switch {
	case a.UserID != 0:
		return fmt.Sprintf("user:%d", a.UserID)
	case a.TokenID != 0:
		return fmt.Sprintf("token:%d", a.TokenID)
	}
	return a.Kind + ":" + a.Name
}

// cutSQL cuts SQL to AuditSQLMaxBytes on a character boundary.
func cutSQL(sql string) string {
	if len(sql) <= apitypes.AuditSQLMaxBytes {
		return sql
	}
	cut := apitypes.AuditSQLMaxBytes
	for cut > 0 && !utf8.RuneStart(sql[cut]) {
		cut--
	}
	return sql[:cut]
}

// sqlSummary is the start of a statement on one line, for an audit
// summary. Callers redact it first: summaries are visible to everyone who
// may read the audit log, data access or not.
func sqlSummary(sql string) string {
	s := strings.Join(strings.Fields(sql), " ")
	if len(s) > 80 {
		cut := 80
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// auditDetail records a data audit event with its detail row (SQL, rows,
// duration, outcome) in one transaction and publishes it, like audit.
// Failures are logged, never returned.
func (c *Core) auditDetail(ctx context.Context, a Actor, action string, ss svcScope, summary string, d store.AddAuditDetailParams) {
	d.Sql = cutSQL(d.Sql)
	params := store.AddAuditEventWithDetailParams{
		Actor: a.Name, ActorKind: a.Kind, Action: action, ProjectID: &ss.project.ID, EnvironmentID: &ss.env.ID,
		Target: ss.String(), Summary: summary, CreatedAt: now(),
	}
	if a.UserID != 0 {
		params.UserID = &a.UserID
	}
	if a.TokenID != 0 {
		params.TokenID = &a.TokenID
	}
	var row store.AuditEvent
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		id, err := q.AddAuditDetail(ctx, d)
		if err != nil {
			return err
		}
		params.DetailID = &id
		row, err = q.AddAuditEventWithDetail(ctx, params)
		return err
	})
	if err != nil {
		c.log.Error("write audit event", "action", action, "err", err)
		return
	}
	ae := apitypes.AuditEvent{
		ID: row.ID, Actor: row.Actor, ActorKind: row.ActorKind, Action: row.Action, Project: ss.project.Name,
		Environment: ss.env.Name, Target: row.Target, Summary: row.Summary, DetailID: row.DetailID, CreatedAt: row.CreatedAt,
	}
	c.bus.publish(busEvent{projectID: ss.project.ID, envID: ss.env.ID, ev: apitypes.Event{
		Type: apitypes.EventAudit, Project: ae.Project, Environment: ae.Environment, Audit: &ae,
	}})
}

// auditQuery records one SQL statement (data.query) with its outcome. The
// summary shows the statement with its values redacted; the full text is
// in the detail (GetAuditDetail).
func (c *Core) auditQuery(ctx context.Context, a Actor, ss svcScope, sql string, stats data.QueryStats, err error) {
	outcome := stats.Outcome
	if err != nil && outcome == apitypes.OutcomeOK {
		outcome = apitypes.OutcomeError
		if ctx.Err() != nil {
			outcome = apitypes.OutcomeCanceled
		}
	}
	summary := fmt.Sprintf("ran SQL (%s, %d rows): %s", outcome, stats.Rows, sqlSummary(c.data.Redact(sql)))
	c.auditDetail(context.WithoutCancel(ctx), a, apitypes.AuditDataQuery, ss, summary, store.AddAuditDetailParams{
		Sql: sql, Rows: stats.Rows, DurationMs: stats.Duration.Milliseconds(), Outcome: string(outcome),
	})
}

// auditBrowse records reading a table page (data.browse), at most once per
// actor and table every browseInterval. The summary names the filtered
// columns and operators, never the values: it is visible to everyone who
// may read the audit log.
func (c *Core) auditBrowse(ctx context.Context, a Actor, ss svcScope, req apitypes.DataPageRequest) {
	if !c.browse.due(browseKey{apitypes.AuditDataBrowse, actorKey(a), ss.svc.ID, req.Table}, time.Now()) {
		return
	}
	summary := "browsed " + req.Table
	var conds []string
	for _, f := range req.Filters {
		conds = append(conds, f.Column+" "+string(f.Op))
	}
	if len(conds) > 0 {
		summary += " filter: " + strings.Join(conds, " and ")
	}
	if req.Order != "" {
		summary += " order: " + req.Order
	}
	c.audit(ctx, a, apitypes.AuditDataBrowse, &ss.project, &ss.env, ss.String(), summary)
}

// auditReveal records a page that revealed a table's sensitive columns
// (data.reveal), at most once per actor and table every browseInterval.
// A table without sensitive columns reveals nothing and isn't audited.
func (c *Core) auditReveal(ctx context.Context, a Actor, dt *dbTarget, table string) {
	var cols []string
	for _, s := range dt.sensitive {
		if s.Schema+"."+s.Table == table {
			cols = append(cols, s.Column)
		}
	}
	if len(cols) == 0 || !c.browse.due(browseKey{apitypes.AuditDataReveal, actorKey(a), dt.ss.svc.ID, table}, time.Now()) {
		return
	}
	c.audit(ctx, a, apitypes.AuditDataReveal, &dt.ss.project, &dt.ss.env, dt.ss.String(),
		fmt.Sprintf("revealed %s: %s", table, strings.Join(cols, ", ")))
}

// GetAuditDetail returns an audit event's detail by its ID
// (AuditEvent.DetailID), to anyone who may see the event: limited actors
// only in their project or environment. The detail of a data.* event (the
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
	var pid, eid int64
	if ev.ProjectID != nil {
		pid = *ev.ProjectID
	}
	if ev.EnvironmentID != nil {
		eid = *ev.EnvironmentID
	}
	if a.limited() && (pid == 0 || !a.canSee(pid, eid)) {
		return apitypes.AuditDetail{}, notFound("audit detail %d not found", id)
	}
	redact := false
	if strings.HasPrefix(ev.Action, "data.") {
		if redact, err = c.dataDetailAccess(ctx, a, ev); err != nil {
			return apitypes.AuditDetail{}, err
		}
	}
	d, err := c.store.GetAuditDetail(ctx, id)
	if err != nil {
		return apitypes.AuditDetail{}, err
	}
	out := toAuditDetail(d)
	if redact {
		out.SQL = c.data.Redact(out.SQL)
	}
	return out, nil
}

// dataDetailAccess checks that a may read the detail of data event ev
// (level read in its environment) and says whether its values must be
// redacted: for callers who see sensitive columns masked, unless it is
// their own event (they wrote the SQL) or the database has none.
func (c *Core) dataDetailAccess(ctx context.Context, a Actor, ev store.AuditEvent) (bool, error) {
	if ev.ProjectID == nil || ev.EnvironmentID == nil {
		return false, errf(apitypes.CodeForbidden, "the detail of this %s event isn't tied to an environment", ev.Action)
	}
	env, err := c.store.GetEnvironment(ctx, *ev.EnvironmentID)
	if err != nil {
		return false, err
	}
	p, err := c.store.GetProject(ctx, *ev.ProjectID)
	if err != nil {
		return false, err
	}
	es := envScope{p, env}
	perm, err := c.dataPermission(ctx, a, es)
	if err != nil {
		return false, err
	}
	if err := datapanel.RequireLevel(perm, apitypes.LevelRead, "reading the SQL of "+ev.Action+" events"); err != nil {
		return false, err
	}
	if perm.Sensitive == apitypes.SensitiveVisible || ownEvent(a, ev) {
		return false, nil
	}
	name, ok := strings.CutPrefix(ev.Target, es.String()+"/")
	if !ok {
		return true, nil
	}
	svc, err := c.store.GetServiceByName(ctx, store.GetServiceByNameParams{EnvironmentID: env.ID, Name: name})
	if store.IsNotFound(err) {
		return true, nil // gone: it can't say what's sensitive
	}
	if err != nil {
		return false, err
	}
	cols, err := c.store.ListSensitiveColumns(ctx, svc.ID)
	return err != nil || len(cols) > 0, err
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
