package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Dock Analytics' query tools (ADR 0037, phase A4): what the Analytics
// tab shows, for agents. All read only, with project read permission;
// replays are listed but watched in the UI, which audits every view.

// SiteInput names an analytics site and the range and filters to read.
type SiteInput struct {
	ProjectInput
	Site    string
	Range   string
	Filters []string
}

// siteQuery resolves a site input.
func siteQuery(c *call, in SiteInput) (string, core.StatsQuery, error) {
	p, err := projectOf(c, in.ProjectInput)
	if err != nil {
		return "", core.StatsQuery{}, err
	}
	if in.Site == "" {
		return "", core.StatsQuery{}, mcpkit.Invalid("pass site (report sites lists them)")
	}
	fs, err := core.ParseFilters(in.Filters)
	return p, core.StatsQuery{Range: in.Range, Filters: fs}, err
}

// storeNote is what a reply says when the store couldn't answer.
func storeNote(online bool, msg string) string {
	if online {
		return ""
	}
	return " The analytics store didn't answer: " + msg + "."
}

// analyticsInput is the analytics tool's input: one report of a site,
// and the arguments only some reports take. One tool rather than one per
// report: every tool costs an agent's context on every turn.
type analyticsInput struct {
	ProjectInput
	Report     string   `json:"report,omitempty" enum:"sites,stats,breakdown,errors,vitals,replays,goals,funnel" doc:"Default stats."`
	Site       string   `json:"site,omitempty" doc:"Usually its domain; sites lists them."`
	Range      string   `json:"range,omitempty" doc:"24h, 7d, 30d, 90d or 12m. Default 30d."`
	Filters    []string `json:"filters,omitempty" doc:"dimension:op:value, op is, is_not or contains, e.g. country:is:NO."`
	Dimension  string   `json:"dimension,omitempty" doc:"breakdown: e.g. page, referrer, channel, utm_source, country, browser, device, event, service, release."`
	Limit      int      `json:"limit,omitempty" doc:"breakdown: default 20."`
	Group      string   `json:"group,omitempty" doc:"errors: one group's detail."`
	By         []string `json:"by,omitempty" doc:"vitals: by these dimensions."`
	Metric     string   `json:"metric,omitempty" doc:"vitals: one over time: lcp, inp, cls, fcp or ttfb."`
	ErrorsOnly bool     `json:"errors_only,omitempty" doc:"replays: only those with an error."`
	Funnel     string   `json:"funnel,omitempty" doc:"funnel: its name; default lists them."`
}

// addAnalyticsTools registers the analytics tool.
func (h *handler) addAnalyticsTools(s *mcpkit.Server[core.Actor]) {
	mcpkit.AddTool(s, "analytics", "Site analytics",
		"Dock Analytics of a project's sites over a range: stats (visitors, views, bounce rate, top pages, sources), breakdown of one dimension, browser errors, Web Vitals (p75), session replays (listed only), goals, funnels; sites lists the sites.",
		mcpkit.ReadOnly, func(ctx context.Context, c *call, in analyticsInput) (mcpkit.Reply, error) {
			if in.Report == "sites" {
				return h.analyticsSites(ctx, c, in.ProjectInput)
			}
			p, q, err := siteQuery(c, SiteInput{ProjectInput: in.ProjectInput, Site: in.Site, Range: in.Range, Filters: in.Filters})
			if err != nil {
				return mcpkit.Reply{}, err
			}
			switch in.Report {
			case "breakdown":
				return h.analyticsBreakdown(ctx, c, p, q, in)
			case "errors":
				return h.analyticsErrors(ctx, c, p, q, in)
			case "vitals":
				return h.analyticsVitals(ctx, c, p, q, in)
			case "replays":
				return h.analyticsReplays(ctx, c, p, q, in)
			case "goals":
				return h.analyticsGoals(ctx, c, p, q, in)
			case "funnel":
				return h.analyticsFunnel(ctx, c, p, q, in)
			}
			return h.analyticsStats(ctx, c, p, q, in)
		})
}

// analyticsSites lists a project's sites.
func (h *handler) analyticsSites(ctx context.Context, c *call, in ProjectInput) (mcpkit.Reply, error) {
	p, err := projectOf(c, in)
	if err != nil {
		return mcpkit.Reply{}, err
	}
	sites, err := h.core.ListSites(ctx, c.Actor, p)
	if err != nil {
		return mcpkit.Reply{}, err
	}
	names := make([]string, 0, len(sites))
	for _, st := range sites {
		if !st.Archived {
			names = append(names, st.Name+" ("+st.Environment+")")
		}
	}
	return mcpkit.Reply{Summary: fmt.Sprintf("%d live sites in %s: %s.", len(names), p, list(names)), Data: sites}, nil
}

// analyticsStats is a site's dashboard.
func (h *handler) analyticsStats(ctx context.Context, c *call, p string, q core.StatsQuery, in analyticsInput) (mcpkit.Reply, error) {
	st, err := h.core.SiteStats(ctx, c.Actor, p, in.Site, q)
	if err != nil {
		return mcpkit.Reply{}, err
	}
	o := st.Overview
	sum := fmt.Sprintf("%s, %s: %d visitors (%d before), %d page views, %d sessions, bounce rate %.0f%%, average session %s.",
		st.Site, rangeText(st.From, st.To), o.Visitors, st.Previous.Visitors, o.Pageviews, o.Sessions, o.BounceRate*100,
		(time.Duration(o.DurationMS) * time.Millisecond).Round(time.Second))
	if pages := st.Breakdowns["page"]; len(pages) > 0 {
		sum += " Top page " + pages[0].Value + "."
	}
	return mcpkit.Reply{Summary: sum + storeNote(st.StoreOnline, st.StoreError) + noticeText(st.Notice), Data: st}, nil
}

// analyticsBreakdown is the top values of one dimension.
func (h *handler) analyticsBreakdown(ctx context.Context, c *call, p string, q core.StatsQuery, in analyticsInput) (mcpkit.Reply, error) {
	if in.Dimension == "" {
		return mcpkit.Reply{}, mcpkit.Invalid("pass dimension, e.g. referrer")
	}
	b, err := h.core.SiteBreakdown(ctx, c.Actor, p, in.Site, analytics.Dimension(in.Dimension), q, or(in.Limit, 20))
	if err != nil {
		return mcpkit.Reply{}, err
	}
	top := make([]string, 0, 5)
	for _, r := range b.Rows[:min(len(b.Rows), 5)] {
		top = append(top, fmt.Sprintf("%s (%d visitors)", or(r.Label, r.Value), r.Visitors))
	}
	return mcpkit.Reply{Summary: fmt.Sprintf("%d values of %s; top: %s.", len(b.Rows), b.Dimension, list(top)), Data: b}, nil
}

// analyticsErrors is a site's browser errors, or one group of them.
func (h *handler) analyticsErrors(ctx context.Context, c *call, p string, q core.StatsQuery, in analyticsInput) (mcpkit.Reply, error) {
	if in.Group != "" {
		d, err := h.core.SiteError(ctx, c.Actor, p, in.Site, in.Group, q)
		if err != nil {
			return mcpkit.Reply{}, err
		}
		return mcpkit.Reply{Summary: fmt.Sprintf("%s: %s, %d times in %d sessions, first seen %s (%s). Top frame: %s.",
			d.Type, d.Message, d.Count, d.Sessions, d.FirstSeen.Format(time.DateTime), or(d.FirstRelease, "unknown release"), or(d.Frame, "none")), Data: d}, nil
	}
	e, err := h.core.SiteErrors(ctx, c.Actor, p, in.Site, q, 50)
	if err != nil {
		return mcpkit.Reply{}, err
	}
	top := make([]string, 0, 5)
	for _, g := range e.Groups[:min(len(e.Groups), 5)] {
		top = append(top, fmt.Sprintf("%s %s: %s (%d)", g.Group, g.Type, g.Message, g.Count))
	}
	sum := fmt.Sprintf("%d error groups on %s, %s: %s.", len(e.Groups), e.Site, rangeText(e.From, e.To), list(top))
	if !e.Collecting {
		sum += " The site doesn't collect errors (its errors_and_vitals setting is off)."
	}
	return mcpkit.Reply{Summary: sum + storeNote(e.StoreOnline, e.StoreError), Data: e}, nil
}

// analyticsVitals is a site's Web Vitals.
func (h *handler) analyticsVitals(ctx context.Context, c *call, p string, q core.StatsQuery, in analyticsInput) (mcpkit.Reply, error) {
	dims := make([]analytics.Dimension, len(in.By))
	for i, d := range in.By {
		dims[i] = analytics.Dimension(d)
	}
	v, err := h.core.SiteVitals(ctx, c.Actor, p, in.Site, core.VitalsQuery{Dimensions: dims, Limit: 20, Metric: analytics.Vital(in.Metric)}, q)
	if err != nil {
		return mcpkit.Reply{}, err
	}
	a := v.All
	sum := fmt.Sprintf("%s, %s, %d page loads at p75: LCP %s, INP %s, CLS %s, FCP %s, TTFB %s.", v.Site, rangeText(v.From, v.To), a.Loads,
		vitalText(v, "lcp", a.LCP), vitalText(v, "inp", a.INP), vitalText(v, "cls", a.CLS), vitalText(v, "fcp", a.FCP), vitalText(v, "ttfb", a.TTFB))
	return mcpkit.Reply{Summary: sum + storeNote(v.StoreOnline, v.StoreError), Data: v}, nil
}

// analyticsReplays lists a site's session replays.
func (h *handler) analyticsReplays(ctx context.Context, c *call, p string, q core.StatsQuery, in analyticsInput) (mcpkit.Reply, error) {
	r, err := h.core.SiteReplays(ctx, c.Actor, p, in.Site, q, in.ErrorsOnly, 50)
	if err != nil {
		return mcpkit.Reply{}, err
	}
	sum := fmt.Sprintf("%d replays on %s, %s.", len(r.Replays), r.Site, rangeText(r.From, r.To))
	if !r.Recording {
		sum += " The site doesn't record replays (its replay setting is off)."
	}
	return mcpkit.Reply{Summary: sum + storeNote(r.StoreOnline, r.StoreError), Data: r}, nil
}

// analyticsGoals is a site's goals.
func (h *handler) analyticsGoals(ctx context.Context, c *call, p string, q core.StatsQuery, in analyticsInput) (mcpkit.Reply, error) {
	g, err := h.core.SiteGoals(ctx, c.Actor, p, in.Site, q)
	if err != nil {
		return mcpkit.Reply{}, err
	}
	lines := make([]string, len(g.Goals))
	for i, gl := range g.Goals {
		lines[i] = fmt.Sprintf("%s %d visitors (%.1f%%)", gl.Name, gl.Visitors, gl.Rate*100)
	}
	return mcpkit.Reply{Summary: fmt.Sprintf("%d goals on %s of %d visitors: %s.", len(g.Goals), g.Site, g.Visitors, list(lines)) +
		storeNote(g.StoreOnline, g.StoreError), Data: g}, nil
}

// analyticsFunnel lists a site's funnels, or reads one.
func (h *handler) analyticsFunnel(ctx context.Context, c *call, p string, q core.StatsQuery, in analyticsInput) (mcpkit.Reply, error) {
	if in.Funnel == "" {
		fs, err := h.core.SiteFunnels(ctx, c.Actor, p, in.Site)
		if err != nil {
			return mcpkit.Reply{}, err
		}
		names := make([]string, len(fs))
		for i, f := range fs {
			names[i] = f.Name
		}
		return mcpkit.Reply{Summary: fmt.Sprintf("%d funnels: %s.", len(fs), list(names)), Data: fs}, nil
	}
	f, err := h.core.SiteFunnel(ctx, c.Actor, p, in.Site, in.Funnel, q)
	if err != nil {
		return mcpkit.Reply{}, err
	}
	steps := make([]string, len(f.Steps))
	for i, st := range f.Steps {
		steps[i] = fmt.Sprintf("%s:%s %d (%.0f%%)", st.Kind, st.Match, st.Sessions, st.Conversion*100)
	}
	return mcpkit.Reply{Summary: fmt.Sprintf("Funnel %s, %s: %s.", f.Name, rangeText(f.From, f.To), strings.Join(steps, " > ")) +
		storeNote(f.StoreOnline, f.StoreError), Data: f}, nil
}

// rangeText is a range as a reply says it.
func rangeText(from, to int64) string {
	f, t := time.UnixMilli(from).UTC(), time.UnixMilli(to).UTC()
	return f.Format("Jan 2 15:04") + " to " + t.Format("Jan 2 15:04") + " UTC"
}

// noticeText is a notice as a sentence, or nothing.
func noticeText(n string) string {
	if n == "" {
		return ""
	}
	return " Note: " + n + "."
}

// vitalText is a p75 with its rating, or "none".
func vitalText(v apitypes.SiteVitals, name string, st apitypes.VitalStat) string {
	if st.P75 < 0 {
		return "none"
	}
	val := fmt.Sprintf("%.0f ms", st.P75)
	if name == "cls" {
		val = fmt.Sprintf("%.2f", st.P75)
	}
	t := v.Thresholds[name]
	switch {
	case st.P75 <= t.Good:
		return val + " (good)"
	case st.P75 > t.Poor:
		return val + " (poor)"
	}
	return val + " (needs improvement)"
}

// or is v, or def when v is the zero value.
func or[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}
