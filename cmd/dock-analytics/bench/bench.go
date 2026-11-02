package bench

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// Querier is what a dashboard asks of a store.
type Querier interface {
	Overview(ctx context.Context, q analytics.Query) (analytics.Overview, error)
	Series(ctx context.Context, q analytics.SeriesQuery) ([]analytics.Point, error)
	Breakdown(ctx context.Context, q analytics.BreakdownQuery) ([]analytics.Row, error)
}

// ErrorsQuerier is a store with errors, Web Vitals, goals and funnels,
// which Run times too (the SQLite comparison has none of them).
type ErrorsQuerier interface {
	Errors(ctx context.Context, q analytics.ErrorsQuery) (analytics.ErrorsResult, error)
	Vitals(ctx context.Context, q analytics.VitalsQuery) (analytics.VitalsResult, error)
	Goals(ctx context.Context, q analytics.GoalsQuery) ([]analytics.GoalResult, error)
	Funnel(ctx context.Context, q analytics.FunnelQuery) ([]analytics.FunnelStep, error)
}

// GoalsAndFunnels is the Filter name of the goals view's timings: five
// goals and a three-step funnel.
const GoalsAndFunnels = "goals and funnels"

// benchGoals are the goals and funnel steps the bench counts: pages and
// events the seed has.
var benchGoals = []analytics.GoalSpec{
	{Kind: analytics.GoalPage, Match: "/pricing"}, {Kind: analytics.GoalPage, Match: "/signup"},
	{Kind: analytics.GoalPage, Match: "/blog/*"}, {Kind: analytics.GoalEvent, Match: "signup"},
	{Kind: analytics.GoalEvent, Match: "event-3"},
}

// ErrorsAndVitals is the Filter name of the errors and Web Vitals views'
// timings: the error groups with their series, and the vitals by page
// and device with theirs.
const ErrorsAndVitals = "errors and vitals"

// DashboardDimensions are the breakdowns a dashboard shows on first load:
// one panel each, with the other dimensions behind tabs.
var DashboardDimensions = []analytics.Dimension{
	analytics.DimPage, analytics.DimEntryPage, analytics.DimReferrer, analytics.DimChannel,
	analytics.DimCountry, analytics.DimBrowser, analytics.DimOS, analytics.DimDevice, analytics.DimEvent,
}

// Range is a dashboard's time range and its series' buckets.
type Range struct {
	Name   string
	Span   time.Duration
	Bucket analytics.Bucket
}

// Ranges are the ones the spike times.
var Ranges = []Range{
	{"24h", 24 * time.Hour, analytics.BucketHour},
	{"30d", 30 * 24 * time.Hour, analytics.BucketDay},
	{"365d", 365 * 24 * time.Hour, analytics.BucketDay},
}

// PickRanges returns the Ranges named in a comma-separated list.
func PickRanges(names string) []Range {
	var out []Range
	for _, r := range Ranges {
		if strings.Contains(","+names+",", ","+r.Name+",") {
			out = append(out, r)
		}
	}
	return out
}

// Filters are the ones the spike times, one at a time, on a whole
// dashboard: an event column, a page, a session's source, a location, a
// substring, a session's entry page and the service.
var Filters = []analytics.Filter{
	{Dimension: analytics.DimCountry, Op: analytics.OpIs, Value: "NO"},
	{Dimension: analytics.DimPage, Op: analytics.OpIs, Value: "/pricing"},
	{Dimension: analytics.DimReferrer, Op: analytics.OpIs, Value: "google.com"},
	{Dimension: analytics.DimBrowser, Op: analytics.OpIs, Value: "Firefox"},
	{Dimension: analytics.DimUTMSource, Op: analytics.OpIs, Value: "newsletter"},
	{Dimension: analytics.DimPage, Op: analytics.OpContains, Value: "blog"},
	{Dimension: analytics.DimEntryPage, Op: analytics.OpIs, Value: "/"},
	{Dimension: analytics.DimService, Op: analytics.OpIs, Value: "11"},
}

// Options say how to time.
type Options struct {
	// End is the end of every range: the seed's End.
	End time.Time
	// Runs is how often each dashboard is loaded; percentiles are over
	// them.
	Runs int
	// Parallel is how many of a dashboard's queries run at once, as the
	// API runs them.
	Parallel int
	Ranges   []Range
	Filters  []analytics.Filter
	Out      io.Writer
}

// Result is one dashboard's timings.
type Result struct {
	Range  string
	Filter string
	// P50 and P95 are of the whole dashboard's wall time.
	P50, P95 time.Duration
	// SlowestQuery is the slowest single query's p95, and which it was.
	SlowestQuery     time.Duration
	SlowestQueryName string
}

// Run loads each range's dashboard opts.Runs times, unfiltered and with
// each filter, and reports the timings.
func Run(ctx context.Context, q Querier, opts Options) ([]Result, error) {
	var out []Result
	for _, rg := range opts.Ranges {
		base := analytics.Query{Site: MainSite, From: opts.End.Add(-rg.Span).UnixMilli(), To: opts.End.UnixMilli()}
		cases := append([]*analytics.Filter{nil}, ptrs(opts.Filters)...)
		for _, f := range cases {
			qq := base
			name := "none"
			if f != nil {
				qq.Filters = []analytics.Filter{*f}
				name = fmt.Sprintf("%s %s %s", f.Dimension, f.Op, f.Value)
			}
			res, err := timeDashboard(ctx, q, qq, rg, opts)
			if err != nil {
				return out, fmt.Errorf("%s, filter %s: %w", rg.Name, name, err)
			}
			res.Range, res.Filter = rg.Name, name
			if opts.Out != nil {
				_, _ = fmt.Fprintf(opts.Out, "%-5s %-28s dashboard p50 %7.1f ms  p95 %7.1f ms  slowest query p95 %7.1f ms (%s)\n",
					res.Range, res.Filter, ms(res.P50), ms(res.P95), ms(res.SlowestQuery), res.SlowestQueryName)
			}
			out = append(out, res)
		}
		eq, ok := q.(ErrorsQuerier)
		if !ok {
			continue
		}
		res, err := timeJobs(ctx, []job{
			{"errors", func() error {
				_, err := eq.Errors(ctx, analytics.ErrorsQuery{Query: base, Bucket: rg.Bucket, Limit: 50})
				return err
			}},
			{"vitals", func() error {
				_, err := eq.Vitals(ctx, analytics.VitalsQuery{Query: base, Dimensions: []analytics.Dimension{analytics.DimPage, analytics.DimDevice},
					Limit: 20, Metric: analytics.VitalLCP, Bucket: rg.Bucket})
				return err
			}},
		}, opts)
		if err != nil {
			return out, fmt.Errorf("%s, %s: %w", rg.Name, ErrorsAndVitals, err)
		}
		res.Range, res.Filter = rg.Name, ErrorsAndVitals
		out = append(out, report(opts, res))
		res, err = timeJobs(ctx, []job{
			{"goals", func() error {
				_, err := eq.Goals(ctx, analytics.GoalsQuery{Query: base, Goals: benchGoals})
				return err
			}},
			{"funnel", func() error {
				_, err := eq.Funnel(ctx, analytics.FunnelQuery{Query: base, Steps: []analytics.GoalSpec{
					{Kind: analytics.GoalPage, Match: "/"}, {Kind: analytics.GoalPage, Match: "/pricing"}, {Kind: analytics.GoalEvent, Match: "signup"}}})
				return err
			}},
		}, opts)
		if err != nil {
			return out, fmt.Errorf("%s, %s: %w", rg.Name, GoalsAndFunnels, err)
		}
		res.Range, res.Filter = rg.Name, GoalsAndFunnels
		out = append(out, report(opts, res))
	}
	return out, nil
}

// report prints a view's timing to opts.Out, if set, and returns it.
func report(opts Options, res Result) Result {
	if opts.Out != nil {
		_, _ = fmt.Fprintf(opts.Out, "%-5s %-28s views     p50 %7.1f ms  p95 %7.1f ms  slowest query p95 %7.1f ms (%s)\n",
			res.Range, res.Filter, ms(res.P50), ms(res.P95), ms(res.SlowestQuery), res.SlowestQueryName)
	}
	return res
}

// job is one of a dashboard's queries.
type job struct {
	name string
	run  func() error
}

// ptrs points at each of fs.
func ptrs(fs []analytics.Filter) []*analytics.Filter {
	out := make([]*analytics.Filter, len(fs))
	for i := range fs {
		out[i] = &fs[i]
	}
	return out
}

// timeDashboard loads one dashboard opts.Runs times (after one warm-up):
// the overview for the range and the one before it, the series and the
// breakdowns, opts.Parallel queries at a time.
func timeDashboard(ctx context.Context, q Querier, qq analytics.Query, rg Range, opts Options) (Result, error) {
	prev := qq
	prev.From, prev.To = qq.From-(qq.To-qq.From), qq.From
	jobs := []job{
		{"overview", func() error { _, err := q.Overview(ctx, qq); return err }},
		{"overview previous", func() error { _, err := q.Overview(ctx, prev); return err }},
		{"series", func() error {
			_, err := q.Series(ctx, analytics.SeriesQuery{Query: qq, Bucket: rg.Bucket})
			return err
		}},
	}
	for _, d := range DashboardDimensions {
		jobs = append(jobs, job{string(d), func() error {
			_, err := q.Breakdown(ctx, analytics.BreakdownQuery{Query: qq, Dimension: d, Limit: 10})
			return err
		}})
	}
	return timeJobs(ctx, jobs, opts)
}

// timeJobs runs jobs opts.Runs times (after one warm-up), opts.Parallel
// at a time, and times each run and each job.
func timeJobs(ctx context.Context, jobs []job, opts Options) (Result, error) {
	per := make([][]time.Duration, len(jobs))
	var walls []time.Duration
	for run := range opts.Runs + 1 {
		start := time.Now()
		sem := make(chan struct{}, max(1, opts.Parallel))
		var wg sync.WaitGroup
		errs := make([]error, len(jobs))
		times := make([]time.Duration, len(jobs))
		for i, j := range jobs {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				t := time.Now()
				errs[i] = j.run()
				times[i] = time.Since(t)
			}()
		}
		wg.Wait()
		wall := time.Since(start)
		for i, err := range errs {
			if err != nil {
				return Result{}, fmt.Errorf("%s: %w", jobs[i].name, err)
			}
		}
		if run == 0 {
			continue // warm-up: the first load reads from disk
		}
		walls = append(walls, wall)
		for i := range jobs {
			per[i] = append(per[i], times[i])
		}
	}
	res := Result{P50: pct(walls, 50), P95: pct(walls, 95)}
	for i, ts := range per {
		if p := pct(ts, 95); p > res.SlowestQuery {
			res.SlowestQuery, res.SlowestQueryName = p, jobs[i].name
		}
	}
	return res, nil
}

// pct is the p-th percentile of ds (nearest rank).
func pct(ds []time.Duration, p int) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	i := (len(s)*p + 99) / 100
	return s[max(0, min(len(s)-1, i-1))]
}

// ms is d in milliseconds.
func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
