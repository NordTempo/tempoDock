package datapanel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// Database insights (ADR 0069): the engine reads statistics as
// the monitor role; the Panel decides which views the caller may see
// (overview, tables and indexes at level schema; queries and sessions,
// which carry statement texts, at read), redacts texts for callers who
// see sensitive columns masked, audits looks at texts like browsing, and
// creates the monitor role on first use for databases set up before it
// existed. Nothing is stored or cached: concurrent identical reads of one
// database share one trip to it.

// Insights reads how the database is doing. No views in req means every
// view the caller may see; views they may not see come back in
// Unavailable with the level they need, and asking only for those is
// forbidden.
func (p *Panel[D]) Insights(ctx context.Context, a Actor, db *Database[D], req apitypes.DataInsightsRequest) (apitypes.DataInsights, error) {
	req, denied, err := insightsRequest(db.Perm, req)
	if err != nil {
		return apitypes.DataInsights{}, err
	}
	tg, err := p.ready(ctx, db, apitypes.LevelSchema, "reading database insights")
	if err != nil {
		return apitypes.DataInsights{}, err
	}
	ins, err := p.flights.do(ctx, flightKey{db.ID, fmt.Sprint(req.Views), req.Order, req.Limit}, func() (apitypes.DataInsights, error) {
		return withMonitor(ctx, p, tg, func() (apitypes.DataInsights, error) {
			return tg.eng.Insights(ctx, tg.t, tg.call(a, false), req)
		})
	})
	if err != nil {
		return apitypes.DataInsights{}, p.dataError(db, err)
	}
	// The result may be shared with another caller: copy what is changed.
	ins.Unavailable = slices.Clone(ins.Unavailable)
	for _, v := range denied {
		ins.Unavailable = append(ins.Unavailable, apitypes.DataUnavailable{View: v,
			Reason: fmt.Sprintf("needs data access level %s; yours is %s (%s)", v.Level(), db.Perm.Level, Why(db.Perm))})
	}
	masked := db.Perm.Sensitive != apitypes.SensitiveVisible
	ins.Sessions = slices.Clone(ins.Sessions)
	for i := range ins.Sessions {
		ins.Sessions[i].Text = p.insightText(db.Engine, ins.Sessions[i].Text, masked)
	}
	ins.Queries = slices.Clone(ins.Queries)
	for i := range ins.Queries {
		ins.Queries[i].Text = p.insightText(db.Engine, ins.Queries[i].Text, masked)
	}
	if slices.Contains(req.Views, apitypes.InsightQueries) || slices.Contains(req.Views, apitypes.InsightSessions) {
		if p.browse.due(browseKey{apitypes.AuditDataInsights, ActorKey(a), db.ID, ""}, time.Now()) {
			p.audit(ctx, a, db, apitypes.AuditDataInsights, "looked at the database's sessions and queries", nil)
		}
	}
	return ins, nil
}

// insightsRequest checks a request and splits its views into those the
// caller may see (req.Views, in the panel's order) and those they may not.
func insightsRequest(perm apitypes.DataPermission, req apitypes.DataInsightsRequest) (apitypes.DataInsightsRequest, []apitypes.DataInsightView, error) {
	switch {
	case req.Limit < 0 || req.Limit > apitypes.DataInsightQueriesMaxLimit:
		return req, nil, invalid("limit must be between 1 and %d", apitypes.DataInsightQueriesMaxLimit)
	case req.Limit == 0:
		req.Limit = apitypes.DataInsightQueriesLimit
	}
	switch req.Order {
	case "":
		req.Order = apitypes.OrderTotal
	case apitypes.OrderTotal, apitypes.OrderMean, apitypes.OrderCalls, apitypes.OrderRows:
	default:
		return req, nil, invalid("order must be total, mean, calls or rows")
	}
	for _, v := range req.Views {
		if !slices.Contains(apitypes.DataInsightViews, v) {
			return req, nil, invalid("unknown view %q; the views are overview, queries, sessions, tables and indexes", v)
		}
	}
	asked := len(req.Views) > 0
	var views, denied []apitypes.DataInsightView
	for _, v := range apitypes.DataInsightViews {
		switch {
		case asked && !slices.Contains(req.Views, v):
		case perm.Level.Includes(v.Level()):
			views = append(views, v)
		case asked:
			denied = append(denied, v)
		}
	}
	if len(views) == 0 {
		need := apitypes.LevelSchema
		if len(denied) > 0 {
			need = denied[0].Level()
		}
		return req, nil, RequireLevel(perm, need, "reading database insights")
	}
	req.Views = views
	return req, denied, nil
}

// insightText is a statement text as the caller may see it: values
// redacted for masked callers, and for everyone when it mentions a
// password (ALTER ROLE ... PASSWORD carries one in clear).
func (p *Panel[D]) insightText(engine apitypes.DataEngine, text string, masked bool) string {
	if text != "" && (masked || strings.Contains(strings.ToUpper(text), "PASSWORD")) {
		return p.redact(engine, text)
	}
	return text
}

// Pressure reads a database's connection counts for the host itself
// (deploy diagnosis), without an actor: counts only, never texts, so it
// isn't audited. ErrNotRunning, not set up and unreachable databases are
// errors; a full one is Full.
func (p *Panel[D]) Pressure(ctx context.Context, db *Database[D]) (apitypes.DataPressure, error) {
	tg, err := p.load(ctx, db)
	if err != nil {
		return apitypes.DataPressure{}, err
	}
	if tg.access == nil {
		return apitypes.DataPressure{}, p.errNotSetUp(db)
	}
	if err := p.connect(ctx, tg); err != nil {
		return apitypes.DataPressure{}, err
	}
	pr, err := withMonitor(ctx, p, tg, func() (apitypes.DataPressure, error) { return tg.eng.Pressure(ctx, tg.t) })
	return pr, p.dataError(db, err)
}

// withMonitor runs fn, and when the monitor can't log in (a database set
// up before the role existed, or its password from an older setup)
// creates or updates it as the owner and runs fn once more. Setup
// consented to the panel's roles; the monitor is one of them.
func withMonitor[D, T any](ctx context.Context, p *Panel[D], tg *target[D], fn func() (T, error)) (T, error) {
	out, err := fn()
	if !errors.Is(err, data.ErrNotSetUp) {
		return out, err
	}
	if err := tg.eng.EnsureMonitor(ctx, tg.t, tg.conn.Owner); err != nil {
		var zero T
		return zero, fmt.Errorf("create %s's monitor role in %s: %w", p.words.Product, tg.db.Name, err)
	}
	return fn()
}

// flightKey identifies one insights read of one database.
type flightKey struct {
	db    int64
	views string
	order apitypes.DataStatementOrder
	limit int
}

// flights lets concurrent identical reads share one trip to the
// database: ten people opening Health at once cost it one read. Entries
// live only while a read is in flight.
type flights struct {
	mu sync.Mutex
	m  map[flightKey]*flight
}

type flight struct {
	done chan struct{}
	res  apitypes.DataInsights
	err  error
}

// do runs fn, or waits for the identical read already running. A waiter
// whose leader was canceled runs its own.
func (f *flights) do(ctx context.Context, k flightKey, fn func() (apitypes.DataInsights, error)) (apitypes.DataInsights, error) {
	f.mu.Lock()
	if c, ok := f.m[k]; ok {
		f.mu.Unlock()
		select {
		case <-c.done:
		case <-ctx.Done():
			return apitypes.DataInsights{}, ctx.Err()
		}
		if errors.Is(c.err, context.Canceled) || errors.Is(c.err, context.DeadlineExceeded) {
			return fn()
		}
		return c.res, c.err
	}
	c := &flight{done: make(chan struct{})}
	if f.m == nil {
		f.m = map[flightKey]*flight{}
	}
	f.m[k] = c
	f.mu.Unlock()
	c.res, c.err = fn()
	f.mu.Lock()
	delete(f.m, k)
	f.mu.Unlock()
	close(c.done)
	return c.res, c.err
}

// EnableQueryStats creates the query stats extension in the database, as
// the owner, when its server loads it. It writes to the user's database,
// so it is for admins (Manage), like setup. A server that doesn't load it
// gets the host's words on what to change (Words.QueryStatsHint).
func (p *Panel[D]) EnableQueryStats(ctx context.Context, a Actor, db *Database[D]) (apitypes.DataQueryStatsResult, error) {
	if !db.Perm.Manage {
		return apitypes.DataQueryStatsResult{}, forbidden("turning on query stats is for admins (%s)", Why(db.Perm))
	}
	tg, err := p.load(ctx, db)
	if err != nil {
		return apitypes.DataQueryStatsResult{}, err
	}
	if err := p.connect(ctx, tg); err != nil {
		return apitypes.DataQueryStatsResult{}, err
	}
	state, created, err := tg.eng.EnableQueryStats(ctx, tg.t, tg.conn.Owner)
	switch {
	case err != nil:
		return apitypes.DataQueryStatsResult{}, p.dataError(db, err)
	case state == apitypes.QueryStatsNotLoaded:
		return apitypes.DataQueryStatsResult{}, invalid("the server doesn't load pg_stat_statements; %s", p.words.QueryStatsHint)
	case !created:
		return apitypes.DataQueryStatsResult{State: "on", Detail: "Query stats were already on."}, nil
	}
	p.audit(ctx, a, db, apitypes.AuditDataSetup, "turned on query stats: created the pg_stat_statements extension", nil)
	return apitypes.DataQueryStatsResult{State: "on", Detail: "Query stats are on; statements count from now."}, nil
}

// ResetQueryStats sets the query stats back to zero. Admins (Manage);
// audited as data.stats_reset.
func (p *Panel[D]) ResetQueryStats(ctx context.Context, a Actor, db *Database[D]) error {
	if !db.Perm.Manage {
		return forbidden("resetting query stats is for admins (%s)", Why(db.Perm))
	}
	tg, err := p.load(ctx, db)
	if err != nil {
		return err
	}
	if err := p.connect(ctx, tg); err != nil {
		return err
	}
	if err := tg.eng.ResetQueryStats(ctx, tg.t, tg.conn.Owner); err != nil {
		return p.dataError(db, err)
	}
	p.audit(ctx, a, db, apitypes.AuditDataStatsReset, "reset the query stats", nil)
	return nil
}

// CancelSession cancels a session's query, or ends the session. It
// changes what the database does, so it needs level write (which tokens
// never have on production: there it is propose); ending a session on
// production also confirms with the environment's or connection's name.
// Audited as data.cancel or data.terminate, the statement in the detail
// as the caller may see it.
func (p *Panel[D]) CancelSession(ctx context.Context, a Actor, db *Database[D], session string, req apitypes.DataCancelRequest) (apitypes.DataCancelResult, error) {
	verb := "cancelling a query"
	if req.Terminate {
		verb = "ending a session"
	}
	if err := RequireLevel(db.Perm, apitypes.LevelWrite, verb); err != nil {
		return apitypes.DataCancelResult{}, err
	}
	if req.Terminate && db.Perm.ConfirmCommit && req.Confirm != db.ConfirmName {
		return apitypes.DataCancelResult{}, invalid("%s is production: ending a session needs confirm set to %s (%q)", db.Name, p.words.ConfirmWhat, db.ConfirmName)
	}
	tg, err := p.load(ctx, db)
	if err != nil {
		return apitypes.DataCancelResult{}, err
	}
	if err := p.connect(ctx, tg); err != nil {
		return apitypes.DataCancelResult{}, err
	}
	res, err := tg.eng.Cancel(ctx, tg.t, tg.conn.Owner, session, req.QueryStartedAt, req.Terminate)
	if err != nil {
		return apitypes.DataCancelResult{}, p.dataError(db, err)
	}
	if !res.Found {
		if req.Terminate {
			return apitypes.DataCancelResult{}, conflict("that session already ended; refresh the sessions")
		}
		return apitypes.DataCancelResult{}, conflict("that query already finished; refresh the sessions")
	}
	who := res.Application
	if who == "" {
		who = "a session"
	}
	action, summary := apitypes.AuditDataCancel, fmt.Sprintf("cancelled a query of %s (%s), running %s", who, res.User, res.Running.Round(time.Second))
	if req.Terminate {
		action, summary = apitypes.AuditDataTerminate, fmt.Sprintf("ended a session of %s (%s), in its transaction for %s", who, res.User, res.Running.Round(time.Second))
	}
	text := p.insightText(db.Engine, res.Text, db.Perm.Sensitive != apitypes.SensitiveVisible)
	p.audit(context.WithoutCancel(ctx), a, db, action, summary, &AuditDetail{SQL: text, Duration: res.Running, Outcome: apitypes.OutcomeOK})
	return apitypes.DataCancelResult{
		Session: session, Terminated: req.Terminate, User: res.User, Application: res.Application, RunningMs: res.Running.Milliseconds(),
	}, nil
}
