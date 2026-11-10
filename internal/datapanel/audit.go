package datapanel

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// The panel's audit events (ADR 0016 section 7), written through
// Host.Audit. Summaries never carry values: everyone who may read the
// host's audit log sees them. The SQL of queries and commits goes in the
// detail.

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
	action   string
	actor    string
	database int64
	table    string
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

// ActorKey identifies an actor across requests: it keys the browse
// throttle and binds sealed cursors, and Dock keys its tunnel limit by it.
func ActorKey(a Actor) string {
	switch {
	case a.UserID != 0:
		return "user:" + strconv.FormatInt(a.UserID, 10)
	case a.TokenID != 0:
		return "token:" + strconv.FormatInt(a.TokenID, 10)
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

// audit records a data event through the host, its detail's SQL cut to
// AuditSQLMaxBytes.
func (p *Panel[D]) audit(ctx context.Context, a Actor, db *Database[D], action, summary string, detail *AuditDetail) {
	if detail != nil {
		detail.SQL = cutSQL(detail.SQL)
	}
	p.host.Audit(ctx, a, db.Host, AuditEntry{Action: action, Summary: summary, Detail: detail})
}

// auditQuery records one SQL statement (data.query) with its outcome. The
// summary shows the statement with its values redacted; the full text is
// in the detail.
func (p *Panel[D]) auditQuery(ctx context.Context, a Actor, db *Database[D], sql string, stats data.QueryStats, err error) {
	outcome := stats.Outcome
	if err != nil && outcome == apitypes.OutcomeOK {
		outcome = apitypes.OutcomeError
		if ctx.Err() != nil {
			outcome = apitypes.OutcomeCanceled
		}
	}
	summary := fmt.Sprintf("ran SQL (%s, %d rows): %s", outcome, stats.Rows, sqlSummary(p.redact(db.Engine, sql)))
	p.audit(context.WithoutCancel(ctx), a, db, apitypes.AuditDataQuery, summary, &AuditDetail{
		SQL: sql, Rows: stats.Rows, Duration: stats.Duration, Outcome: outcome,
	})
}

// auditBrowse records reading a table page (data.browse), at most once per
// actor and table every browseInterval. The summary names the filtered
// columns and operators, never the values.
func (p *Panel[D]) auditBrowse(ctx context.Context, a Actor, db *Database[D], req apitypes.DataPageRequest) {
	if !p.browse.due(browseKey{apitypes.AuditDataBrowse, ActorKey(a), db.ID, req.Table}, time.Now()) {
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
	p.audit(ctx, a, db, apitypes.AuditDataBrowse, summary, nil)
}

// auditReveal records a page that revealed a table's sensitive columns
// (data.reveal), at most once per actor and table every browseInterval.
// A table without sensitive columns reveals nothing and isn't audited.
func (p *Panel[D]) auditReveal(ctx context.Context, a Actor, tg *target[D], table string) {
	var cols []string
	for _, s := range tg.sensitive {
		if s.Schema+"."+s.Table == table {
			cols = append(cols, s.Column)
		}
	}
	if len(cols) == 0 || !p.browse.due(browseKey{apitypes.AuditDataReveal, ActorKey(a), tg.db.ID, table}, time.Now()) {
		return
	}
	p.audit(ctx, a, tg.db, apitypes.AuditDataReveal, fmt.Sprintf("revealed %s: %s", table, strings.Join(cols, ", ")), nil)
}

// RedactDetail is an audit detail of a data event as a caller may see it:
// with its values redacted when the caller sees sensitive columns masked
// (perm), it isn't their own event and the database has sensitive
// columns. engine is the database's, "" when it is gone (the whole
// statement is hidden then). The host checks the caller may read the
// detail at all.
func (p *Panel[D]) RedactDetail(engine apitypes.DataEngine, perm apitypes.DataPermission, own, sensitive bool, d apitypes.AuditDetail) apitypes.AuditDetail {
	if perm.Sensitive != apitypes.SensitiveVisible && !own && sensitive {
		d.SQL = p.redact(engine, d.SQL)
	}
	return d
}
