package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// StatsQuery is what a site's dashboard asks for: a named range ending
// now (24h, 7d, 30d, 90d, 12m), or From and To (Unix ms) for a custom
// one, and filters.
type StatsQuery struct {
	Range    string
	From, To int64
	Filters  []analytics.Filter
}

// statsRanges are the named ranges.
var statsRanges = map[string]time.Duration{
	"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
	"90d": 90 * 24 * time.Hour, "12m": 365 * 24 * time.Hour,
}

// dashboardDimensions are the breakdowns a dashboard loads at once (the
// rest are a SiteBreakdown away).
var dashboardDimensions = []analytics.Dimension{
	analytics.DimPage, analytics.DimEntryPage, analytics.DimReferrer, analytics.DimChannel, analytics.DimCountry,
	analytics.DimBrowser, analytics.DimDevice, analytics.DimEvent, analytics.DimService,
}

// dashboardRows is how many values each dashboard breakdown shows.
const dashboardRows = 10

// dashboardParallel is how many of a dashboard's store queries are in
// flight at once.
const dashboardParallel = 4

// span works out q's range, defaulting to the last 30 days.
func (q StatsQuery) span(t time.Time) (from, to int64, err error) {
	if q.From != 0 || q.To != 0 {
		if q.To <= q.From {
			return 0, 0, invalid("the range is empty: from must be before to")
		}
		return q.From, q.To, nil
	}
	name := q.Range
	if name == "" {
		name = "30d"
	}
	d, ok := statsRanges[name]
	if !ok {
		return 0, 0, invalid("range %q isn't one of 24h, 7d, 30d, 90d, 12m; or give from and to", q.Range)
	}
	return t.Add(-d).UnixMilli(), t.UnixMilli(), nil
}

// checkFilters refuses filters the store doesn't know, with advice.
func checkFilters(fs []analytics.Filter) error {
	if len(fs) > analytics.MaxFilters {
		return invalid("at most %d filters", analytics.MaxFilters)
	}
	for _, f := range fs {
		known := false
		for _, d := range analytics.Dimensions {
			known = known || d == f.Dimension
		}
		if !known {
			return invalid("%q isn't a dimension; use one of %s", f.Dimension, dimensionList())
		}
		switch f.Op {
		case analytics.OpIs, analytics.OpIsNot, analytics.OpContains:
		default:
			return invalid("filter operator %q isn't is, is_not or contains", f.Op)
		}
	}
	return nil
}

// dimensionList names every dimension, for messages.
func dimensionList() string {
	names := make([]string, len(analytics.Dimensions))
	for i, d := range analytics.Dimensions {
		names[i] = string(d)
	}
	return strings.Join(names, ", ")
}

// SiteStats returns a site's dashboard. A store that can't be reached
// gives empty numbers and StoreOnline false, not an error: the events
// wait in the servers' spools.
func (c *Core) SiteStats(ctx context.Context, a Actor, project, name string, q StatsQuery) (apitypes.SiteStats, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "reading a site's analytics")
	if err != nil {
		return apitypes.SiteStats{}, err
	}
	if err := checkFilters(q.Filters); err != nil {
		return apitypes.SiteStats{}, err
	}
	t := now()
	from, to, err := q.span(t)
	if err != nil {
		return apitypes.SiteStats{}, err
	}
	out := apitypes.SiteStats{Site: s.site.Name, From: from, To: to, Bucket: "day", Series: []apitypes.AnalyticsPoint{},
		Breakdowns: map[string][]apitypes.AnalyticsRow{}, Markers: []apitypes.SiteMarker{}}
	if to-from <= 2*24*time.Hour.Milliseconds() {
		out.Bucket = "hour"
	}
	if out.Markers, err = c.siteMarkers(ctx, s.site.ID, from, to); err != nil {
		return out, err
	}
	// Daily totals outlive the raw events; filters read raw events only.
	if len(q.Filters) > 0 {
		defaults, err := c.siteDefaults(ctx)
		if err != nil {
			return out, err
		}
		days := *mergeSettings(defaults, siteOverrides(s.site)).EventRetentionDays
		if from < t.AddDate(0, 0, -days).UnixMilli() {
			out.Notice = fmt.Sprintf("filters only see the last %d days, the site's event retention; without filters the whole range counts", days)
		}
	}
	cl, _ := c.analyticsClient()
	if cl == nil {
		out.StoreError = "analytics is off; it starts when a hostname collects"
		return out, nil
	}
	query := analytics.Query{Site: s.site.ID, From: from, To: to, Filters: q.Filters}
	prev := query
	prev.From, prev.To = from-(to-from), from
	// The store runs queries one at a time; asking for a few at once saves
	// the round trips to an analytics server elsewhere.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(dashboardParallel)
	g.Go(func() error {
		o, err := cl.Overview(gctx, query)
		out.Overview = toOverview(o)
		return err
	})
	g.Go(func() error {
		o, err := cl.Overview(gctx, prev)
		out.Previous = toOverview(o)
		return err
	})
	g.Go(func() error {
		pts, err := cl.Series(gctx, analytics.SeriesQuery{Query: query, Bucket: analytics.Bucket(out.Bucket)})
		for _, p := range pts {
			out.Series = append(out.Series, apitypes.AnalyticsPoint(p))
		}
		return err
	})
	rows := make([][]analytics.Row, len(dashboardDimensions))
	for i, d := range dashboardDimensions {
		g.Go(func() (err error) {
			rows[i], err = cl.Breakdown(gctx, analytics.BreakdownQuery{Query: query, Dimension: d, Limit: dashboardRows})
			return err
		})
	}
	err = g.Wait()
	if err == nil {
		for i, d := range dashboardDimensions {
			out.Breakdowns[string(d)] = c.toRows(ctx, d, rows[i])
		}
	}
	return c.storeAnswer(out, err)
}

// storeAnswer keeps a dashboard with an unreachable store as an answer
// saying so; other errors are errors.
func (c *Core) storeAnswer(out apitypes.SiteStats, err error) (apitypes.SiteStats, error) {
	var se *analytics.StoreError
	switch {
	case err == nil:
		out.StoreOnline = true
		return out, nil
	case errors.As(err, &se) && se.Status == 400:
		return out, invalid("%s", se.Message)
	case errors.Is(err, analytics.ErrOffline), errors.Is(err, analytics.ErrBusy), CodeOf(err) == apitypes.CodeServerOffline:
		out.StoreError = "the analytics server is offline or starting; events wait on each server until it is back"
		if errors.Is(err, analytics.ErrBusy) {
			out.StoreError = analytics.ErrBusy.Error()
		}
		return out, nil
	}
	return out, err
}

// SiteBreakdown returns up to limit values of one dimension.
func (c *Core) SiteBreakdown(ctx context.Context, a Actor, project, name string, dim analytics.Dimension, q StatsQuery, limit int) (apitypes.SiteBreakdown, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "reading a site's analytics")
	if err != nil {
		return apitypes.SiteBreakdown{}, err
	}
	if err := checkFilters(q.Filters); err != nil {
		return apitypes.SiteBreakdown{}, err
	}
	if !slices.Contains(analytics.Dimensions, dim) {
		return apitypes.SiteBreakdown{}, invalid("%q isn't a dimension; use one of %s", dim, dimensionList())
	}
	from, to, err := q.span(now())
	if err != nil {
		return apitypes.SiteBreakdown{}, err
	}
	cl, _ := c.analyticsClient()
	if cl == nil {
		return apitypes.SiteBreakdown{}, errf(apitypes.CodeServerOffline, "analytics is off; it starts when a hostname collects")
	}
	rows, err := cl.Breakdown(ctx, analytics.BreakdownQuery{
		Query: analytics.Query{Site: s.site.ID, From: from, To: to, Filters: q.Filters}, Dimension: dim, Limit: min(max(limit, 1), analytics.MaxBreakdown),
	})
	if errors.Is(err, analytics.ErrOffline) || errors.Is(err, analytics.ErrBusy) {
		return apitypes.SiteBreakdown{}, errf(apitypes.CodeServerOffline, "the analytics server is offline or busy; try again in a moment")
	}
	if err != nil {
		return apitypes.SiteBreakdown{}, err
	}
	return apitypes.SiteBreakdown{Dimension: string(dim), Rows: c.toRows(ctx, dim, rows)}, nil
}

// toOverview converts the store's overview.
func toOverview(o analytics.Overview) apitypes.AnalyticsOverview {
	return apitypes.AnalyticsOverview{Visitors: o.Visitors, Pageviews: o.Pageviews, Sessions: o.Sessions,
		BounceRate: o.BounceRate, DurationMS: o.Duration, Events: o.Events}
}

// toRows converts breakdown rows; services are named, not numbered.
func (c *Core) toRows(ctx context.Context, d analytics.Dimension, rows []analytics.Row) []apitypes.AnalyticsRow {
	out := make([]apitypes.AnalyticsRow, len(rows))
	for i, r := range rows {
		out[i] = apitypes.AnalyticsRow(r)
	}
	if d != analytics.DimService || len(rows) == 0 {
		return out
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		if id, err := strconv.ParseInt(r.Value, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	names, err := c.store.ServiceNames(ctx, ids)
	if err != nil {
		return out
	}
	byID := map[string]string{}
	for _, n := range names {
		byID[strconv.FormatInt(n.ID, 10)] = n.Name
	}
	for i := range out {
		if n, ok := byID[out[i].Value]; ok {
			out[i].Value = n
		}
	}
	return out
}

// siteMarkers are a site's markers in [from, to): its own (hostnames
// added, moved, removed) and its services' deploys.
func (c *Core) siteMarkers(ctx context.Context, siteID, from, to int64) ([]apitypes.SiteMarker, error) {
	// The range is in milliseconds; its last one counts whole.
	f, t := time.UnixMilli(from).UTC(), time.UnixMilli(to).Add(time.Millisecond).UTC()
	ms, err := c.store.ListSiteMarkers(ctx, store.ListSiteMarkersParams{SiteID: siteID, FromAt: f, ToAt: t})
	if err != nil {
		return nil, err
	}
	out := []apitypes.SiteMarker{}
	for _, m := range ms {
		out = append(out, apitypes.SiteMarker{At: m.At, Kind: m.Kind, Message: m.Message})
	}
	deploys, err := c.store.ListSiteDeploys(ctx, store.ListSiteDeploysParams{SiteID: siteID, FromAt: &f, ToAt: &t})
	if err != nil {
		return nil, err
	}
	for _, d := range deploys {
		if d.FinishedAt != nil {
			out = append(out, apitypes.SiteMarker{At: *d.FinishedAt, Kind: "deploy", Message: d.ServiceName + " r" + strconv.FormatInt(d.Number, 10)})
		}
	}
	return out, nil
}
