package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// SpanWriter is where seeded spans go.
type SpanWriter interface {
	WriteSpans(ctx context.Context, source string, position uint64, spans []analytics.Span, lim analytics.TraceLimits) (analytics.SpanWriteResult, error)
}

// TraceQuerier is what the trace views and the Metrics tab ask of a
// store.
type TraceQuerier interface {
	Traces(ctx context.Context, q analytics.TracesQuery) (analytics.TracesResult, error)
	Trace(ctx context.Context, q analytics.TraceQuery) (analytics.TraceDetail, error)
	MetricNames(ctx context.Context, q analytics.MetricNamesQuery) (analytics.MetricNamesResult, error)
	QueryMetrics(ctx context.Context, q analytics.MetricsQuery) (analytics.MetricsResult, error)
}

// SeedSpansOptions shape the synthetic traces.
type SeedSpansOptions struct {
	// Spans is about how many spans to write, over Days days ending at
	// End (the store takes spans for its last 30 days only).
	Spans int
	Days  int
	End   time.Time
	Seed  uint64
	// Batch is the spans per write; 0 is 10,000.
	Batch int
	// Source names the writes' spool; "" is "seed-spans".
	Source string
	// Progress, when set, is called after every day.
	Progress func(day, days, written int)
}

// Seeded traces: org 1 has TraceServices services, the first of them
// (BusyService) taking 60% of the traffic; org 2 has two more.
const (
	TraceOrg      = 1
	TraceServices = 18
	BusyService   = 1
	traceRoutes   = 30
)

// SeedSpans writes about opts.Spans spans as traces of a web service:
// Caddy's edge span, the service's server span, its database and cache
// calls, and for a fifth of them a call to a second service with its own.
// Traces are about 8 spans, durations are long-tailed, 3% fail and a
// service deploys once a day. Spans go in time order, in batches.
func SeedSpans(ctx context.Context, w SpanWriter, opts SeedSpansOptions) (int, error) {
	r := rand.New(rand.NewPCG(opts.Seed, opts.Seed^0x7ace))
	if opts.Batch <= 0 {
		opts.Batch = 10_000
	}
	if opts.Source == "" {
		opts.Source = "seed-spans"
	}
	perDay := float64(opts.Spans) / float64(opts.Days)
	start := opts.End.Add(-time.Duration(opts.Days) * 24 * time.Hour)
	batch := make([]analytics.Span, 0, opts.Batch+64)
	var pos uint64
	written := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		pos++
		res, err := w.WriteSpans(ctx, opts.Source, pos, batch, analytics.TraceLimits{})
		if err != nil {
			return err
		}
		written += res.Written
		batch = batch[:0]
		return nil
	}
	for day := range opts.Days {
		dayStart := start.Add(time.Duration(day) * 24 * time.Hour)
		var made float64
		for made < perDay {
			at := dayStart.Add(time.Duration(made / perDay * float64(24*time.Hour)))
			before := len(batch)
			batch = seedTrace(r, batch, at, day)
			made += float64(len(batch) - before)
			if len(batch) >= opts.Batch {
				if err := flush(); err != nil {
					return written, err
				}
			}
		}
		if opts.Progress != nil {
			opts.Progress(day+1, opts.Days, written+len(batch))
		}
	}
	return written, flush()
}

// seedTrace appends one trace starting at at.
func seedTrace(r *rand.Rand, out []analytics.Span, at time.Time, day int) []analytics.Span {
	org, service := int64(TraceOrg), int64(BusyService)
	switch x := r.Float64(); {
	case x < 0.05:
		org, service = 2, TraceServices+1+int64(r.IntN(2))
	case x > 0.6:
		service = 2 + int64(r.IntN(TraceServices-1))
	}
	var trace analytics.TraceID
	for i := range trace {
		trace[i] = byte(r.Uint32())
	}
	route := zipf(r, traceRoutes)
	method := "GET"
	if route%4 == 3 {
		method = "POST"
	}
	path := fmt.Sprintf("/r%d/:id", route)
	failed := r.Float64() < 0.03
	// Long-tailed: most requests a few ms to tens of ms, some seconds.
	total := time.Duration(math.Exp(r.NormFloat64()*1.1+math.Log(float64(8+route)))) * time.Millisecond
	ids := r.Uint64() | 1
	next := func() analytics.SpanID { ids += 0x9e3779b97f4a7c15; return analytics.SpanID(ids) }
	base := func(svc int64, id, parent analytics.SpanID, start time.Time, dur time.Duration) analytics.Span {
		return analytics.Span{
			Org: org, Project: 1 + svc/6, Env: 1 + svc/3, Service: svc, Release: 1000 + svc*100 + int64(day),
			Trace: trace, ID: id, Parent: parent, Start: start.UnixMicro(), Duration: max(dur.Microseconds(), 1),
		}
	}
	code := 200
	if failed {
		code = 500
	}
	kept := analytics.KeptSampled
	if failed {
		kept = analytics.KeptError
	} else if total > time.Second {
		kept = analytics.KeptSlow
	}
	edgeID, serverID := next(), next()
	edge := base(service, edgeID, 0, at, total+time.Millisecond)
	edge.Kind, edge.Entry, edge.Name, edge.Method, edge.Route, edge.HTTPStatus, edge.Kept = analytics.SpanEdge, true, method+" "+path, method, path, code, kept
	out = append(out, edge)
	server := base(service, serverID, edgeID, at.Add(200*time.Microsecond), total)
	server.Kind, server.Entry, server.Name, server.Method, server.Route, server.HTTPStatus, server.Kept = analytics.SpanServer, true, method+" "+path, method, path, code, kept
	server.Attrs = []byte(fmt.Sprintf(`{"http.route":%q,"url.scheme":"http","server.port":8080,"user_agent.original":"Mozilla/5.0"}`, path))
	if failed {
		server.Status, server.ErrorFP = analytics.StatusError, fmt.Sprintf("fp%d", route%5)
		server.Events = []byte(`[{"name":"exception","attrs":{"exception.type":"QueryFailedError","exception.message":"canceling statement due to statement timeout"}}]`)
	}
	out = append(out, server)
	// The service's own calls take up most of its time, in sequence.
	calls := 2 + r.IntN(6)
	t := at.Add(time.Millisecond)
	for i := range calls {
		dur := total / time.Duration(calls+1)
		sp := base(service, next(), serverID, t, dur)
		sp.Kind, sp.Kept = analytics.SpanClient, kept
		if i%3 == 2 {
			sp.Name = "redis GET"
			sp.Attrs = []byte(`{"db.system":"redis","db.operation.name":"GET"}`)
		} else {
			sp.Name = fmt.Sprintf("SELECT t%d", (route+i)%12)
			sp.Attrs = []byte(fmt.Sprintf(`{"db.system":"postgresql","db.query.text":"SELECT * FROM t%d WHERE id = $1 AND org_id = $2","server.address":"postgres"}`, (route+i)%12))
		}
		if failed && i == calls-1 {
			sp.Status, sp.ErrorFP = analytics.StatusError, server.ErrorFP
		}
		out = append(out, sp)
		t = t.Add(dur)
	}
	// A fifth call another service.
	if r.IntN(5) == 0 {
		other := 2 + (service+int64(route))%int64(TraceServices-1)
		if org == 2 {
			other = TraceServices + 1 + (service+1)%2
		}
		dur := total / 3
		client := base(service, next(), serverID, t, dur)
		client.Kind, client.Name, client.Kept = analytics.SpanClient, "POST other", kept
		inner := base(other, next(), client.ID, t.Add(100*time.Microsecond), dur-200*time.Microsecond)
		inner.Kind, inner.Entry, inner.Name, inner.Method, inner.Route, inner.HTTPStatus, inner.Kept = analytics.SpanServer, true, "POST /internal", "POST", "/internal", 200, kept
		db := base(other, next(), inner.ID, t.Add(300*time.Microsecond), dur/2)
		db.Kind, db.Name, db.Kept = analytics.SpanClient, "INSERT jobs", kept
		db.Attrs = []byte(`{"db.system":"postgresql","db.query.text":"INSERT INTO jobs (kind, payload) VALUES ($1, $2)"}`)
		out = append(out, client, inner, db)
	}
	return out
}

// zipf picks 0..n-1, the low ones far more often.
func zipf(r *rand.Rand, n int) int {
	return min(n-1, int(math.Pow(float64(n), r.Float64()))-1)
}

// TraceCase is one trace view the bench times.
type TraceCase struct {
	Name  string
	Range string
	P95   time.Duration
	Runs  int
}

// TraceOptions tune RunTraces.
type TraceOptions struct {
	// End is the end of every range and Days the days before it, as
	// the seed had them; 0 is 7.
	End  time.Time
	Days int
	Runs int
	// Metrics times the Metrics tab too, on points SeedMetrics wrote.
	Metrics bool
	// Out, when set, gets a table of the results.
	Out io.Writer
}

// Trace bench case names, which `make perf` budgets by.
const (
	CaseServiceList   = "service list"
	CaseServiceFilter = "service list, filtered"
	CaseOrgList       = "org list, filtered"
	CaseTraceAt       = "trace, with its time"
	CaseTraceAnywhere = "trace, without a time"
	// The Metrics tab, on SeedMetrics' busy service: its list and a
	// chart of each seeded metric, and one metric split by an
	// attribute.
	CaseMetricsDashboard = "metrics dashboard"
	CaseMetricBy         = "metric by attribute"
)

// RunTraces times the trace views on a store SeedSpans filled: the busy
// service's list (newest and slowest) over an hour, a day and a week,
// filtered by route, by errors and by release, the org's list of errors
// over every service, and opening traces from the list with and without
// their time.
func RunTraces(ctx context.Context, q TraceQuerier, opts TraceOptions) ([]TraceCase, error) {
	if opts.Runs <= 0 {
		opts.Runs = 10
	}
	if opts.Days <= 0 {
		opts.Days = 7
	}
	// The busy service's last release: the seed's last day.
	lastRelease := int64(1000 + BusyService*100 + opts.Days - 1)
	orgServices := make([]int64, TraceServices)
	for i := range orgServices {
		orgServices[i] = int64(i + 1)
	}
	var out []TraceCase
	time1 := func(name, rng string, run func() error) error {
		ds := make([]time.Duration, 0, opts.Runs)
		for range opts.Runs {
			t := time.Now()
			if err := run(); err != nil {
				return fmt.Errorf("%s over %s: %w", name, rng, err)
			}
			ds = append(ds, time.Since(t))
		}
		c := TraceCase{Name: name, Range: rng, P95: pct(ds, 95), Runs: opts.Runs}
		out = append(out, c)
		if opts.Out != nil {
			_, _ = fmt.Fprintf(opts.Out, "%-24s %-4s p95 %8.1f ms\n", c.Name, c.Range, ms(c.P95))
		}
		return nil
	}
	var found []analytics.TraceSummary
	for _, rg := range []struct {
		name string
		span time.Duration
	}{{"1h", time.Hour}, {"24h", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}} {
		base := analytics.TracesQuery{Org: TraceOrg, Services: []int64{BusyService}, From: opts.End.Add(-rg.span).UnixMicro(), To: opts.End.UnixMicro()}
		for _, sort := range []analytics.TraceSort{analytics.SortNewest, analytics.SortSlowest} {
			q1 := base
			q1.Sort = sort
			if err := time1(CaseServiceList, rg.name, func() error {
				res, err := q.Traces(ctx, q1)
				if sort == analytics.SortSlowest && rg.name == "7d" {
					found = res.Traces
				}
				return err
			}); err != nil {
				return out, err
			}
		}
		for _, f := range []func(*analytics.TracesQuery){
			func(x *analytics.TracesQuery) { x.Route, x.Method = "/r3/:id", "POST" },
			func(x *analytics.TracesQuery) { x.Status = analytics.TraceError },
			func(x *analytics.TracesQuery) { x.SinceRelease, x.Sort = lastRelease, analytics.SortSlowest },
		} {
			q1 := base
			f(&q1)
			if err := time1(CaseServiceFilter, rg.name, func() error { _, err := q.Traces(ctx, q1); return err }); err != nil {
				return out, err
			}
		}
		q1 := base
		q1.Services, q1.Status, q1.Sort = orgServices, analytics.TraceError, analytics.SortSlowest
		if err := time1(CaseOrgList, rg.name, func() error { _, err := q.Traces(ctx, q1); return err }); err != nil {
			return out, err
		}
	}
	if len(found) == 0 {
		return out, fmt.Errorf("the busy service has no traces in the week before %s: seed with -end there", opts.End.Format(time.DateOnly))
	}
	i := 0
	pick := func() analytics.TraceSummary { i++; return found[i%len(found)] }
	if err := time1(CaseTraceAt, "7d", func() error {
		t := pick()
		res, err := q.Trace(ctx, analytics.TraceQuery{Org: TraceOrg, Trace: t.Trace, At: t.Start})
		if err == nil && len(res.Spans) == 0 {
			err = fmt.Errorf("trace %s has no spans", t.Trace)
		}
		return err
	}); err != nil {
		return out, err
	}
	if err := time1(CaseTraceAnywhere, "7d", func() error {
		t := pick()
		_, err := q.Trace(ctx, analytics.TraceQuery{Org: TraceOrg, Trace: t.Trace})
		return err
	}); err != nil {
		return out, err
	}
	if !opts.Metrics {
		return out, nil
	}
	var charts []analytics.MetricChartQuery
	for _, m := range metricNames {
		charts = append(charts, analytics.MetricChartQuery{Name: m.name})
	}
	for _, rg := range []struct {
		name       string
		span, step time.Duration
	}{{"1h", time.Hour, time.Minute}, {"24h", 24 * time.Hour, 20 * time.Minute}, {"7d", 7 * 24 * time.Hour, 2 * time.Hour}} {
		mq := analytics.MetricsQuery{Org: TraceOrg, Service: BusyService, From: opts.End.Add(-rg.span).Unix(), To: opts.End.Unix(), Step: int64(rg.step.Seconds()), Charts: charts}
		if err := time1(CaseMetricsDashboard, rg.name, func() error {
			names, err := q.MetricNames(ctx, analytics.MetricNamesQuery{Org: TraceOrg, Services: []int64{BusyService}, From: mq.From, To: mq.To})
			if err == nil && len(names.Metrics) == 0 {
				err = errors.New("the busy service has no metrics: seed-traces -metric-series")
			}
			if err == nil {
				_, err = q.QueryMetrics(ctx, mq)
			}
			return err
		}); err != nil {
			return out, err
		}
		by := mq
		by.Charts = []analytics.MetricChartQuery{{Name: "http.client.request.duration", Agg: analytics.AggP95, By: "route"}}
		if err := time1(CaseMetricBy, rg.name, func() error { _, err := q.QueryMetrics(ctx, by); return err }); err != nil {
			return out, err
		}
	}
	return out, nil
}
