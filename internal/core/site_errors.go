package core

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Errors and Web Vitals (ADR 0037, phase A2): what a site's script
// reports when its errors_and_vitals setting is on, read from the store
// like the dashboard. Filters take the dimensions errors and page loads
// record (analytics.ErrorDimensions).

// siteRead is a site's query as the store takes it, with the dashboard's
// bucket for the range and whether the site collects errors and vitals.
type siteRead struct {
	site       siteScope
	query      analytics.Query
	bucket     string
	collecting bool
	cl         *analytics.Client
}

// readSite loads a site for an errors or vitals read and checks q.
func (c *Core) readSite(ctx context.Context, a Actor, project, name string, q StatsQuery) (siteRead, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "reading a site's analytics")
	if err != nil {
		return siteRead{}, err
	}
	if len(q.Filters) > analytics.MaxFilters {
		return siteRead{}, invalid("at most %d filters", analytics.MaxFilters)
	}
	for _, f := range q.Filters {
		if !slices.Contains(analytics.ErrorDimensions, f.Dimension) {
			return siteRead{}, invalid("errors and Web Vitals filter by %s; %q isn't one of them", errorDimensionList(), f.Dimension)
		}
	}
	if err := checkFilters(q.Filters); err != nil {
		return siteRead{}, err
	}
	if q.Filters, err = c.serviceFilters(ctx, s, q.Filters); err != nil {
		return siteRead{}, err
	}
	from, to, err := q.span(now())
	if err != nil {
		return siteRead{}, err
	}
	defaults, err := c.siteDefaults(ctx)
	if err != nil {
		return siteRead{}, err
	}
	r := siteRead{site: s, query: analytics.Query{Site: s.site.ID, From: from, To: to, Filters: q.Filters}, bucket: "day",
		collecting: *mergeSettings(defaults, siteOverrides(s.site)).ErrorsAndVitals}
	if to-from <= 2*24*time.Hour.Milliseconds() {
		r.bucket = "hour"
	}
	r.cl, _ = c.analyticsClient()
	return r, nil
}

// errorDimensionList names the dimensions errors and vitals take.
func errorDimensionList() string {
	out := ""
	for i, d := range analytics.ErrorDimensions {
		if i > 0 {
			out += ", "
		}
		out += string(d)
	}
	return out
}

// storeStatus sorts a store call's error: a store that can't be reached
// or is busy is an answer saying so (online false, msg), a bad query is
// invalid, and the rest are errors.
func storeStatus(err error) (online bool, msg string, _ error) {
	var se *analytics.StoreError
	switch {
	case err == nil:
		return true, "", nil
	case errors.As(err, &se) && se.Status == 400:
		return false, "", invalid("%s", se.Message)
	case errors.Is(err, analytics.ErrBusy):
		return false, analytics.ErrBusy.Error(), nil
	case errors.Is(err, analytics.ErrOffline), CodeOf(err) == apitypes.CodeServerOffline:
		return false, "the analytics server is offline or starting; events wait on each server until it is back", nil
	}
	return false, "", err
}

// analyticsOff is the store error while analytics hasn't started.
const analyticsOff = "analytics is off; it starts when a hostname collects"

// SiteErrors returns a site's error groups in a range, the most frequent
// first, each with its series. An unreachable store gives no groups and
// StoreOnline false.
func (c *Core) SiteErrors(ctx context.Context, a Actor, project, name string, q StatsQuery, limit int) (apitypes.SiteErrors, error) {
	r, err := c.readSite(ctx, a, project, name, q)
	if err != nil {
		return apitypes.SiteErrors{}, err
	}
	out := apitypes.SiteErrors{Site: r.site.site.Name, From: r.query.From, To: r.query.To, Bucket: r.bucket,
		Groups: []apitypes.ErrorGroup{}, Collecting: r.collecting}
	if r.cl == nil {
		out.StoreError = analyticsOff
		return out, nil
	}
	groups, err := r.cl.Errors(ctx, analytics.ErrorsQuery{Query: r.query, Bucket: analytics.Bucket(r.bucket), Limit: min(max(limit, 1), analytics.MaxErrorGroups)})
	if out.StoreOnline, out.StoreError, err = storeStatus(err); err != nil || !out.StoreOnline {
		return out, err
	}
	ids := make([]int64, len(groups))
	for i, g := range groups {
		ids[i] = g.FirstRelease
	}
	labels := c.releaseLabels(ctx, ids)
	for _, g := range groups {
		out.Groups = append(out.Groups, toErrorGroup(g, labels))
	}
	return out, nil
}

// toErrorGroup converts the store's group.
func toErrorGroup(g analytics.ErrorGroup, labels map[int64]string) apitypes.ErrorGroup {
	out := apitypes.ErrorGroup{Group: g.Group, Type: g.Type, Message: g.Message, Frame: g.Frame, Count: g.Count, Sessions: g.Sessions,
		Visitors: g.Visitors, FirstSeen: time.UnixMilli(g.FirstSeen).UTC(), LastSeen: time.UnixMilli(g.LastSeen).UTC(),
		FirstRelease: labels[g.FirstRelease]}
	for _, p := range g.Series {
		out.Series = append(out.Series, apitypes.CountPoint(p))
	}
	return out
}

// releaseLabels names releases "web r41", by ID; unknown IDs are left out.
func (c *Core) releaseLabels(ctx context.Context, ids []int64) map[int64]string {
	out := map[int64]string{}
	ids = slices.DeleteFunc(slices.Clone(ids), func(id int64) bool { return id == 0 })
	if len(ids) == 0 {
		return out
	}
	slices.Sort(ids)
	names, err := c.store.ReleaseNames(ctx, slices.Compact(ids))
	if err != nil {
		return out
	}
	for _, n := range names {
		out[n.ID] = n.Name + " r" + strconv.FormatInt(n.Number, 10)
	}
	return out
}

// SiteError returns one error group of a site in a range: its counts,
// latest stack, pages, browsers, OS, devices, releases and latest
// occurrences. With the store unreachable it is CodeServerOffline.
func (c *Core) SiteError(ctx context.Context, a Actor, project, name, group string, q StatsQuery) (apitypes.SiteError, error) {
	r, err := c.readSite(ctx, a, project, name, q)
	if err != nil {
		return apitypes.SiteError{}, err
	}
	if r.cl == nil {
		return apitypes.SiteError{}, errf(apitypes.CodeServerOffline, analyticsOff)
	}
	d, err := r.cl.Error(ctx, analytics.ErrorQuery{Query: r.query, Group: group, Bucket: analytics.Bucket(r.bucket)})
	online, msg, err := storeStatus(err)
	if err != nil {
		return apitypes.SiteError{}, err
	}
	if !online {
		return apitypes.SiteError{}, errf(apitypes.CodeServerOffline, "%s", msg)
	}
	ids := []int64{d.FirstRelease}
	for _, o := range d.Recent {
		ids = append(ids, o.Release)
	}
	labels := c.releaseLabels(ctx, ids)
	out := apitypes.SiteError{Site: r.site.site.Name, From: r.query.From, To: r.query.To, Bucket: r.bucket,
		ErrorGroup: toErrorGroup(d.ErrorGroup, labels), Stack: d.Stack,
		Pages: c.toRows(ctx, analytics.DimPage, d.Pages), Browsers: c.toRows(ctx, analytics.DimBrowser, d.Browsers),
		OS: c.toRows(ctx, analytics.DimOS, d.OS), Devices: c.toRows(ctx, analytics.DimDevice, d.Devices),
		Releases: c.toRows(ctx, analytics.DimRelease, d.Releases), Recent: []apitypes.ErrorOccurrence{}}
	for _, o := range d.Recent {
		out.Recent = append(out.Recent, apitypes.ErrorOccurrence{At: time.UnixMilli(o.Time).UTC(), Path: o.Path, Browser: o.Browser,
			BrowserVersion: o.BrowserVersion, OS: o.OS, Device: o.Device, Country: o.Country, Release: labels[o.Release], Session: o.Session})
	}
	return out, nil
}

// VitalsQuery is what a site's Web Vitals view asks for besides its range
// and filters: breakdowns (up to Limit values each), and the vital to
// chart over time (none if empty).
type VitalsQuery struct {
	Dimensions []analytics.Dimension
	Limit      int
	Metric     analytics.Vital
}

// SiteVitals returns a site's Web Vitals in a range: over all page loads,
// by each of v's dimensions and v's metric over time. An unreachable
// store gives empty numbers and StoreOnline false.
func (c *Core) SiteVitals(ctx context.Context, a Actor, project, name string, v VitalsQuery, q StatsQuery) (apitypes.SiteVitals, error) {
	r, err := c.readSite(ctx, a, project, name, q)
	if err != nil {
		return apitypes.SiteVitals{}, err
	}
	for _, d := range v.Dimensions {
		if !slices.Contains(analytics.ErrorDimensions, d) {
			return apitypes.SiteVitals{}, invalid("Web Vitals break down by %s; %q isn't one of them", errorDimensionList(), d)
		}
	}
	if v.Metric != "" && !slices.Contains(analytics.VitalNames, v.Metric) {
		return apitypes.SiteVitals{}, invalid("%q isn't a Web Vital; chart lcp, inp, cls, fcp or ttfb", v.Metric)
	}
	dims := slices.Clone(v.Dimensions)
	slices.Sort(dims)
	dims = slices.Compact(dims)
	out := apitypes.SiteVitals{Site: r.site.site.Name, From: r.query.From, To: r.query.To, Bucket: r.bucket,
		Breakdowns: map[string][]apitypes.VitalsRow{}, Metric: string(v.Metric), Series: []apitypes.VitalPoint{},
		Thresholds: map[string]apitypes.VitalThreshold{}, Collecting: r.collecting}
	for name, t := range analytics.VitalThresholds {
		out.Thresholds[string(name)] = apitypes.VitalThreshold(t)
	}
	none := apitypes.VitalStat{P75: -1}
	out.All = apitypes.VitalsRow{LCP: none, INP: none, CLS: none, FCP: none, TTFB: none}
	if r.cl == nil {
		out.StoreError = analyticsOff
		return out, nil
	}
	res, err := r.cl.Vitals(ctx, analytics.VitalsQuery{Query: r.query, Dimensions: dims, Limit: min(max(v.Limit, 1), analytics.MaxBreakdown),
		Metric: v.Metric, Bucket: analytics.Bucket(r.bucket)})
	if out.StoreOnline, out.StoreError, err = storeStatus(err); err != nil || !out.StoreOnline {
		return out, err
	}
	out.All = toVitalsRow(res.All)
	for d, vrows := range res.Breakdowns {
		var labels map[string]string
		if d == analytics.DimService || d == analytics.DimRelease {
			rows := make([]analytics.Row, len(vrows))
			for i, vr := range vrows {
				rows[i].Value = vr.Value
			}
			labels = map[string]string{}
			for _, l := range c.toRows(ctx, d, rows) {
				labels[l.Value] = l.Label
			}
		}
		out.Breakdowns[string(d)] = make([]apitypes.VitalsRow, 0, len(vrows))
		for _, vr := range vrows {
			row := toVitalsRow(vr)
			row.Label = labels[vr.Value]
			out.Breakdowns[string(d)] = append(out.Breakdowns[string(d)], row)
		}
	}
	for _, p := range res.Series {
		out.Series = append(out.Series, apitypes.VitalPoint(p))
	}
	return out, nil
}

// toVitalsRow converts the store's row.
func toVitalsRow(v analytics.VitalsRow) apitypes.VitalsRow {
	return apitypes.VitalsRow{Value: v.Value, Loads: v.Loads, LCP: apitypes.VitalStat(v.LCP), INP: apitypes.VitalStat(v.INP),
		CLS: apitypes.VitalStat(v.CLS), FCP: apitypes.VitalStat(v.FCP), TTFB: apitypes.VitalStat(v.TTFB)}
}
