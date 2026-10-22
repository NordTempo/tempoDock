package tempobase

import (
	"context"
	"math"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbstore"
	"github.com/NordTempo/tempoDock/internal/tempobase/tbtypes"
)

// pruneInterval is how often, at most, the audit retention pass runs. It
// runs inline when an event is written, never on a timer: an idle server
// prunes nothing and costs nothing.
const pruneInterval = time.Hour

// Audit log pages.
const (
	AuditPageLimit    = 100
	AuditPageMaxLimit = 500
)

// audit records an event; connection is the connection it is about, if
// any. Failures are logged, never returned: the action already happened.
func (a *App) audit(ctx context.Context, act Actor, action string, connection *int64, target, summary string) {
	a.writeAudit(ctx, act, action, connection, target, summary, nil)
}

// writeAudit records an event with its detail row, if any, in one
// transaction, then runs the retention pass when it is due.
func (a *App) writeAudit(ctx context.Context, act Actor, action string, connection *int64, target, summary string, d *datapanel.AuditDetail) {
	ctx = context.WithoutCancel(ctx)
	user, token := act.auditIDs()
	kind := act.Kind
	if kind == "" {
		kind = "system"
	}
	params := tbstore.AddAuditEventParams{
		CreatedAt: a.now(), Actor: act.Name, ActorKind: kind, UserID: user, TokenID: token,
		Action: action, ConnectionID: connection, Target: target, Summary: summary,
	}
	err := a.store.Tx(ctx, func(q *tbstore.Queries) error {
		if d != nil {
			id, err := q.AddAuditDetail(ctx, tbstore.AddAuditDetailParams{
				Sql: cutSQL(d.SQL), Rows: d.Rows, DurationMs: d.Duration.Milliseconds(), Outcome: string(d.Outcome),
			})
			if err != nil {
				return err
			}
			params.DetailID = &id
		}
		return q.AddAuditEvent(ctx, params)
	})
	if err != nil {
		a.log.Error("write audit event", "action", action, "err", err)
		return
	}
	a.prune(ctx)
}

// prune deletes audit events older than the retention, at most 1000 with
// details and 1000 without per pass, when the last pass is over
// pruneInterval ago (in memory: a restart prunes on the first event).
func (a *App) prune(ctx context.Context) {
	n := a.now()
	a.pruneMu.Lock()
	if n.Sub(a.lastPrune) < pruneInterval {
		a.pruneMu.Unlock()
		return
	}
	a.lastPrune = n
	a.pruneMu.Unlock()
	before := n.AddDate(0, 0, -a.cfg.AuditRetentionDays)
	err := a.store.Tx(ctx, func(q *tbstore.Queries) error {
		if err := q.PruneAuditDetails(ctx, before); err != nil {
			return err
		}
		return q.PruneAuditEvents(ctx, before)
	})
	if err != nil {
		a.log.Error("prune audit events", "err", err)
	}
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

// AuditQuery filters the audit log.
type AuditQuery struct {
	// Connection is a connection's name, Action an exact action; empty
	// for all.
	Connection string
	Action     string
	// Before pages back: events with IDs below it (0: the newest).
	Before int64
	Limit  int
}

// ListAudit returns audit events, newest first. Admin only: people see
// their own queries in the SQL tab's history.
func (a *App) ListAudit(ctx context.Context, act Actor, f AuditQuery) ([]tbtypes.AuditEvent, error) {
	if err := act.requireAdmin("reading the audit log"); err != nil {
		return nil, err
	}
	limit := int64(f.Limit)
	if limit <= 0 {
		limit = AuditPageLimit
	}
	limit = min(limit, AuditPageMaxLimit)
	before := f.Before
	if before <= 0 {
		before = math.MaxInt64
	}
	conns, err := a.store.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(conns))
	var connID *int64
	for _, c := range conns {
		names[c.ID] = c.Name
		if c.Name == f.Connection {
			connID = ptr(c.ID)
		}
	}
	var rows []tbstore.AuditEvent
	switch {
	case f.Connection != "" && connID == nil:
		return nil, notFound("connection %q not found; events of removed connections are in the unfiltered log", f.Connection)
	case connID != nil && f.Action != "":
		rows, err = a.store.ListAuditEventsByConnectionAction(ctx, tbstore.ListAuditEventsByConnectionActionParams{ConnectionID: connID, Action: f.Action, BeforeID: before, MaxRows: limit})
	case connID != nil:
		rows, err = a.store.ListAuditEventsByConnection(ctx, tbstore.ListAuditEventsByConnectionParams{ConnectionID: connID, BeforeID: before, MaxRows: limit})
	case f.Action != "":
		rows, err = a.store.ListAuditEventsByAction(ctx, tbstore.ListAuditEventsByActionParams{Action: f.Action, BeforeID: before, MaxRows: limit})
	default:
		rows, err = a.store.ListAuditEvents(ctx, tbstore.ListAuditEventsParams{BeforeID: before, MaxRows: limit})
	}
	if err != nil {
		return nil, err
	}
	out := make([]tbtypes.AuditEvent, len(rows))
	for i, e := range rows {
		out[i] = tbtypes.AuditEvent{
			ID: e.ID, Actor: e.Actor, ActorKind: e.ActorKind, Action: e.Action, Target: e.Target,
			Summary: e.Summary, DetailID: e.DetailID, CreatedAt: e.CreatedAt,
		}
		if e.ConnectionID != nil {
			out[i].Connection = names[*e.ConnectionID]
		}
	}
	return out, nil
}

// GetAuditDetail returns the SQL of audit event id, a data.query or
// data.commit.
// Admin only, like the log; admins see sensitive values, so nothing is
// redacted.
func (a *App) GetAuditDetail(ctx context.Context, act Actor, id int64) (apitypes.AuditDetail, error) {
	if err := act.requireAdmin("reading the audit log"); err != nil {
		return apitypes.AuditDetail{}, err
	}
	d, err := a.store.GetEventAuditDetail(ctx, id)
	if tbstore.IsNotFound(err) {
		return apitypes.AuditDetail{}, notFound("audit event %d has no detail", id)
	}
	if err != nil {
		return apitypes.AuditDetail{}, err
	}
	return toAuditDetail(d), nil
}

func toAuditDetail(d tbstore.AuditDetail) apitypes.AuditDetail {
	return apitypes.AuditDetail{ID: d.ID, SQL: d.Sql, Rows: d.Rows, DurationMS: d.DurationMs, Outcome: apitypes.DataOutcome(d.Outcome)}
}
