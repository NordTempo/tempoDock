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
	"github.com/NordTempo/tempoDock/internal/store"
)

// browseInterval is how often one actor's browsing of one table is
// audited (data.browse): the first page, then at most once per interval.
const browseInterval = 10 * time.Minute

// browseLogMax bounds the browse throttle's memory: past it, entries older
// than browseInterval are dropped when the next one is added.
const browseLogMax = 1024

// browseLog remembers when each actor last had a data.browse event per
// table, in memory only: after a restart the next page is audited again.
// No timer: old entries are swept when the map grows.
type browseLog struct {
	mu   sync.Mutex
	last map[browseKey]time.Time
}

type browseKey struct {
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
// summary.
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

// auditQuery records one SQL statement (data.query) with its outcome.
func (c *Core) auditQuery(ctx context.Context, a Actor, ss svcScope, sql string, stats data.QueryStats, err error) {
	outcome := stats.Outcome
	if err != nil && outcome == apitypes.OutcomeOK {
		outcome = apitypes.OutcomeError
		if ctx.Err() != nil {
			outcome = apitypes.OutcomeCanceled
		}
	}
	summary := fmt.Sprintf("ran SQL (%s, %d rows): %s", outcome, stats.Rows, sqlSummary(sql))
	c.auditDetail(context.WithoutCancel(ctx), a, apitypes.AuditDataQuery, ss, summary, store.AddAuditDetailParams{
		Sql: sql, Rows: stats.Rows, DurationMs: stats.Duration.Milliseconds(), Outcome: string(outcome),
	})
}

// auditBrowse records reading a table page (data.browse), at most once per
// actor and table every browseInterval.
func (c *Core) auditBrowse(ctx context.Context, a Actor, ss svcScope, req apitypes.DataPageRequest) {
	if !c.browse.due(browseKey{actorKey(a), ss.svc.ID, req.Table}, time.Now()) {
		return
	}
	summary := "browsed " + req.Table
	var conds []string
	for _, f := range req.Filters {
		v := f.Value
		if f.Op == apitypes.FilterIn {
			v = strings.Join(f.Values, ",")
		}
		conds = append(conds, strings.TrimSpace(fmt.Sprintf("%s %s %s", f.Column, f.Op, v)))
	}
	if len(conds) > 0 {
		summary += " filter: " + strings.Join(conds, " and ")
	}
	if req.Order != "" {
		summary += " order: " + req.Order
	}
	c.audit(ctx, a, apitypes.AuditDataBrowse, &ss.project, &ss.env, ss.String(), summary)
}

// GetAuditDetail returns an audit event's detail by its ID
// (AuditEvent.DetailID), to anyone who may see the event: limited actors
// only in their project or environment.
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
	d, err := c.store.GetAuditDetail(ctx, id)
	if err != nil {
		return apitypes.AuditDetail{}, err
	}
	return toAuditDetail(d), nil
}

func toAuditDetail(d store.AuditDetail) apitypes.AuditDetail {
	return apitypes.AuditDetail{ID: d.ID, SQL: d.Sql, Rows: d.Rows, DurationMS: d.DurationMs, Outcome: apitypes.DataOutcome(d.Outcome)}
}
