package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// registerAnalytics registers Dock Analytics' platform settings (ADR
// 0037): which server runs the store, and whether it answers.
func registerAnalytics(s *Server, a huma.API) {
	tags := []string{"analytics"}
	authed(s, a, huma.Operation{
		OperationID: "get-analytics", Method: http.MethodGet, Path: Prefix + "/analytics",
		Summary: "Show Dock Analytics' state", Tags: tags,
		Description: "Whether analytics is on, which server runs the store, and whether the store answers (asked now). Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.AnalyticsStatus], error) {
		return reply(s.core.AnalyticsStatus(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-analytics-server", Method: http.MethodPut, Path: Prefix + "/analytics/server",
		Summary: "Choose the analytics server", Tags: tags,
		Description: "Runs the analytics store on the named server (empty: the control plane's own) and turns analytics on. Every server keeps its events in its spool until the store has them. A store on another server stops; its data stays on that server's volume, and the new store starts empty. Operators only.",
		Errors:      []int{http.StatusNotFound},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.SetAnalyticsServerRequest
	}) (*out[apitypes.AnalyticsStatus], error) {
		return reply(s.core.SetAnalyticsServer(ctx, actor, in.Body))
	})
}

// SitePath addresses a site of a project.
type SitePath struct {
	ProjectPath
	Site string `path:"site" doc:"Site name"`
}

// StatsParams pick a dashboard's range and filters.
type StatsParams struct {
	Range  string   `query:"range" enum:"24h,7d,30d,90d,12m," doc:"A range ending now; default 30d"`
	From   int64    `query:"from" doc:"Start of a custom range, Unix ms (with to)"`
	To     int64    `query:"to" doc:"End of a custom range, Unix ms"`
	Filter []string `query:"filter" doc:"dimension:op:value, op is, is_not or contains; repeat for more"`
}

// query parses the parameters for core.
func (p StatsParams) query() (core.StatsQuery, error) {
	q := core.StatsQuery{Range: p.Range, From: p.From, To: p.To}
	for _, f := range p.Filter {
		dim, rest, ok1 := strings.Cut(f, ":")
		op, value, ok2 := strings.Cut(rest, ":")
		if !ok1 || !ok2 {
			return q, huma.Error400BadRequest(fmt.Sprintf("filter %q must look like country:is:NO", f))
		}
		q.Filters = append(q.Filters, analytics.Filter{Dimension: analytics.Dimension(dim), Op: analytics.FilterOp(op), Value: value})
	}
	return q, nil
}

const sitesPath = projectPathT + "/sites"

// registerSites registers Dock Analytics' sites (ADR 0037).
func registerSites(s *Server, a huma.API) {
	tags := []string{"analytics"}
	authed(s, a, huma.Operation{
		OperationID: "list-sites", Method: http.MethodGet, Path: sitesPath,
		Summary: "List a project's analytics sites", Tags: tags,
		Description: "Sets of hostnames whose visits are counted together, live first.",
	}, func(ctx context.Context, actor core.Actor, in *struct{ ProjectPath }) (*out[[]apitypes.Site], error) {
		return reply(s.core.ListSites(ctx, actor, in.Project))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-site", Method: http.MethodGet, Path: sitesPath + "/{site}",
		Summary: "Show an analytics site", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct{ SitePath }) (*out[apitypes.Site], error) {
		return reply(s.core.GetSite(ctx, actor, in.Project, in.Site))
	})
	authed(s, a, huma.Operation{
		OperationID: "update-site", Method: http.MethodPatch, Path: sitesPath + "/{site}",
		Summary: "Rename a site or change its settings", Tags: tags,
		Description: "Settings given are set on the site; reset names settings to set back to the install's defaults. Needs deploy.",
		Errors:      []int{http.StatusConflict},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		Body apitypes.UpdateSiteRequest
	}) (*out[apitypes.Site], error) {
		return reply(s.core.UpdateSite(ctx, actor, in.Project, in.Site, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-site-stats", Method: http.MethodGet, Path: sitesPath + "/{site}/stats",
		Summary: "A site's dashboard", Tags: tags,
		Description: "Visitors, page views, sessions, bounce rate and session length for a range and the one before it, the series, the top values of the dashboard's dimensions, and deploys and domain moves as markers. With the analytics server offline the numbers are empty and store_online is false.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		StatsParams
	}) (*out[apitypes.SiteStats], error) {
		q, err := in.query()
		if err != nil {
			return nil, err
		}
		return reply(s.core.SiteStats(ctx, actor, in.Project, in.Site, q))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-site-breakdown", Method: http.MethodGet, Path: sitesPath + "/{site}/breakdown",
		Summary: "The top values of one dimension", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		StatsParams
		Dimension string `query:"dimension" required:"true" doc:"page, entry_page, exit_page, hostname, referrer, channel, utm_source, country, city, browser, os, device, screen, language, event, outbound, service, release, ..."`
		Limit     int    `query:"limit" minimum:"1" maximum:"1000" default:"100"`
	}) (*out[apitypes.SiteBreakdown], error) {
		q, err := in.query()
		if err != nil {
			return nil, err
		}
		return reply(s.core.SiteBreakdown(ctx, actor, in.Project, in.Site, analytics.Dimension(in.Dimension), q, in.Limit))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-site-errors", Method: http.MethodGet, Path: sitesPath + "/{site}/errors",
		Summary: "A site's errors", Tags: tags,
		Description: "Uncaught errors and unhandled rejections in the range, grouped by type, message and top frame, the most frequent first, each with its series. Filters take page, hostname, country, browser, browser_version, os, device, service and release.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		StatsParams
		Limit int `query:"limit" minimum:"1" maximum:"200" default:"50"`
	}) (*out[apitypes.SiteErrors], error) {
		q, err := in.query()
		if err != nil {
			return nil, err
		}
		return reply(s.core.SiteErrors(ctx, actor, in.Project, in.Site, q, in.Limit))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-site-error", Method: http.MethodGet, Path: sitesPath + "/{site}/errors/{group}",
		Summary: "One error group of a site", Tags: tags,
		Description: "Its counts, latest stack trace, pages, browsers, OS, devices and releases, and its latest occurrences with their sessions, in the range.",
		Errors:      []int{http.StatusBadRequest},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		Group string `path:"group" doc:"The group's fingerprint, 16 hex digits"`
		StatsParams
	}) (*out[apitypes.SiteError], error) {
		q, err := in.query()
		if err != nil {
			return nil, err
		}
		return reply(s.core.SiteError(ctx, actor, in.Project, in.Site, in.Group, q))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-site-vitals", Method: http.MethodGet, Path: sitesPath + "/{site}/vitals",
		Summary: "A site's Web Vitals", Tags: tags,
		Description: "LCP, INP, CLS, FCP and TTFB at the 75th percentile: over all page loads in the range (with good and poor counts), by each dimension's values, and one metric over time.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		StatsParams
		Dimension []string `query:"dimension" doc:"Break down by page, hostname, country, browser, browser_version, os, device, service or release; repeat for more"`
		Limit     int      `query:"limit" minimum:"1" maximum:"1000" default:"20" doc:"Values per breakdown"`
		Metric    string   `query:"metric" enum:"lcp,inp,cls,fcp,ttfb," doc:"The vital to chart over time; empty for no series"`
	}) (*out[apitypes.SiteVitals], error) {
		q, err := in.query()
		if err != nil {
			return nil, err
		}
		dims := make([]analytics.Dimension, len(in.Dimension))
		for i, d := range in.Dimension {
			dims[i] = analytics.Dimension(d)
		}
		return reply(s.core.SiteVitals(ctx, actor, in.Project, in.Site, core.VitalsQuery{Dimensions: dims, Limit: in.Limit, Metric: analytics.Vital(in.Metric)}, q))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-site-replays", Method: http.MethodGet, Path: sitesPath + "/{site}/replays",
		Summary: "A site's session replays", Tags: tags,
		Description: "Recorded visits that began in the range, newest first: a visitor's recorded page loads with no gap over 30 minutes. Filters take page, country, browser, os, device and release.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		StatsParams
		Errors bool `query:"errors" doc:"Only replays with an error"`
		Limit  int  `query:"limit" minimum:"1" maximum:"500" default:"100"`
	}) (*out[apitypes.SiteReplays], error) {
		q, err := in.query()
		if err != nil {
			return nil, err
		}
		return reply(s.core.SiteReplays(ctx, actor, in.Project, in.Site, q, in.Errors, in.Limit))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-site-replay", Method: http.MethodGet, Path: sitesPath + "/{site}/replays/{replay}",
		Summary: "One session replay", Tags: tags,
		Description: "Its page loads and its timeline: page views, custom events and errors. The recording itself is /events.",
		Errors:      []int{http.StatusBadRequest},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		Replay string `path:"replay" doc:"Any of its page loads' IDs, 16 hex digits"`
	}) (*out[apitypes.ReplayDetail], error) {
		return reply(s.core.SiteReplay(ctx, actor, in.Project, in.Site, in.Replay))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-site-replay-events", Method: http.MethodGet, Path: sitesPath + "/{site}/replays/{replay}/events",
		Summary: "A session replay's recording", Tags: tags,
		Description: "Every page load's rrweb events as JSON lines: a {\"segment\": id} line before each page load's chunks, each chunk a JSON array of events. Gzipped when accepted. Every call is audited.",
		Errors:      []int{http.StatusBadRequest},
		Responses: map[string]*huma.Response{"200": {
			Description: "rrweb events",
			Content:     map[string]*huma.MediaType{"application/x-ndjson": {Schema: &huma.Schema{Type: huma.TypeString}}},
		}},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		Replay         string `path:"replay" doc:"Any of its page loads' IDs, 16 hex digits"`
		AcceptEncoding string `header:"Accept-Encoding"`
	}) (*huma.StreamResponse, error) {
		body, zipped, err := s.core.SiteReplayEvents(ctx, actor, in.Project, in.Site, in.Replay, strings.Contains(in.AcceptEncoding, "gzip"))
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			defer func() { _ = body.Close() }()
			hctx.SetHeader("Content-Type", "application/x-ndjson")
			hctx.SetHeader("Cache-Control", "no-store")
			if zipped {
				hctx.SetHeader("Content-Encoding", "gzip")
			}
			hctx.SetStatus(http.StatusOK)
			_, _ = io.Copy(hctx.BodyWriter(), body)
		}}, nil
	})
	authed(s, a, huma.Operation{
		OperationID: "enable-hostname-analytics", Method: http.MethodPut, Path: Prefix + "/analytics/hostnames/{hostname}",
		Summary: "Turn analytics on for a domain", Tags: tags,
		Description: "The hostname joins a site: the one named, else its service's other hostnames' site, else a new one named after it. Its pages then need <script src=\"/_dock/a/s.js\" defer></script>. The first site starts the analytics store. Needs deploy.",
		Errors:      []int{http.StatusConflict},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Hostname string `path:"hostname"`
		Body     apitypes.EnableAnalyticsRequest
	}) (*out[apitypes.Site], error) {
		return reply(s.core.EnableHostnameAnalytics(ctx, actor, in.Hostname, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "disable-hostname-analytics", Method: http.MethodDelete, Path: Prefix + "/analytics/hostnames/{hostname}",
		Summary: "Turn analytics off for a domain", Tags: tags,
		Description: "The site keeps its history; one left without hostnames is archived. Needs deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Hostname string `path:"hostname"`
	}) (*struct{}, error) {
		return nil, s.core.DisableHostnameAnalytics(ctx, actor, in.Hostname)
	})
	authed(s, a, huma.Operation{
		OperationID: "get-analytics-defaults", Method: http.MethodGet, Path: Prefix + "/analytics/defaults",
		Summary: "The install's default site settings", Tags: tags, Description: "Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.SiteSettings], error) {
		return reply(s.core.SiteDefaults(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-analytics-defaults", Method: http.MethodPatch, Path: Prefix + "/analytics/defaults",
		Summary: "Change the install's default site settings", Tags: tags,
		Description: "Settings given become every site's default where the site sets nothing; reset names settings to set back to Dock's own defaults. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.UpdateSiteRequest }) (*out[apitypes.SiteSettings], error) {
		return reply(s.core.SetSiteDefaults(ctx, actor, in.Body.Settings, in.Body.Reset))
	})
}

// registerSiteLive registers a site's live view (ADR 0037).
func registerSiteLive(s *Server, a huma.API) {
	sse(s, a, huma.Operation{
		OperationID: "site-live", Method: http.MethodGet, Path: sitesPath + "/{site}/live",
		Summary: "Watch a site live", Tags: []string{"analytics"},
		Description: "Visitors of the last five minutes and the latest events, now and on every change. Costs nothing while nobody watches.",
	}, []string{"live"}, sseFormat[apitypes.SiteLive]{
		name: func(apitypes.SiteLive) string { return "live" },
	}, func(ctx context.Context, actor core.Actor, in *struct{ SitePath }) (core.Stream[apitypes.SiteLive], error) {
		return s.core.SiteLive(ctx, actor, in.Project, in.Site)
	})
}
