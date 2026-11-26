package api

import (
	"context"
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
		OperationID: "set-analytics", Method: http.MethodPut, Path: Prefix + "/analytics/enabled",
		Summary: "Turn Dock Analytics on or off", Tags: tags,
		Description: "On runs the store on the analytics server again, on the data it kept, and the servers' ingests take events again. Off stops the store's container and the ingests keep nothing, and a site's first domain doesn't turn it on again; with remove_data the store's data (every site's events, replays and errors) goes too. 409 while the store starts or moves; the analytics server must be online. ADR 0101. Operators only.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.AnalyticsSwitch
	}) (*out[apitypes.AnalyticsStatus], error) {
		return reply(s.core.SetAnalytics(ctx, actor, in.Body))
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

// registerTracing registers tracing's platform switch: the trace store
// beside the analytics store, and whether it answers.
func registerTracing(s *Server, a huma.API) {
	tags := []string{"tracing"}
	authed(s, a, huma.Operation{
		OperationID: "get-tracing", Method: http.MethodGet, Path: Prefix + "/tracing",
		Summary: "Show tracing's state", Tags: tags,
		Description: "Whether tracing is on, the server the trace store runs on (the analytics server) and whether it answers (asked now). Operators only.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.TracingStatus], error) {
		return reply(s.core.TracingStatus(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-tracing", Method: http.MethodPut, Path: Prefix + "/tracing",
		Summary: "Turn tracing on or off", Tags: tags,
		Description: "On starts the trace store on the analytics server; it doesn't turn analytics on. Off removes the trace store and every span it holds, and needs the analytics server online. Operators only.",
		Errors:      []int{http.StatusServiceUnavailable},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Body apitypes.TracingSettings
	}) (*out[apitypes.TracingStatus], error) {
		if in.Body.Enabled {
			return reply(s.core.EnableTraces(ctx, actor))
		}
		return reply(s.core.DisableTraces(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "rotate-trace-key", Method: http.MethodPost, Path: Prefix + "/tracing/rotate-key",
		Summary: "Revoke every trace ingest token", Tags: tags,
		Description: "Rotates the trace key: every ingest token minted before stops working at once, and traced services get new ones with their next deploy or restart. Operators only, with a recent sign-in.",
		Errors:      []int{http.StatusConflict},
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.TracingStatus], error) {
		return reply(s.core.RotateTraceKey(ctx, actor))
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
	Filter []string `query:"filter,explode" doc:"dimension:op:value, op is, is_not or contains; repeat for more"`
}

// query parses the parameters for core.
func (p StatsParams) query() (core.StatsQuery, error) {
	q := core.StatsQuery{Range: p.Range, From: p.From, To: p.To}
	fs, err := core.ParseFilters(p.Filter)
	if err != nil {
		return q, huma.Error400BadRequest(err.Error())
	}
	q.Filters = fs
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
		OperationID: "list-org-sites", Method: http.MethodGet, Path: Prefix + "/sites",
		Summary: "List the organization's analytics sites", Tags: tags,
		Description: "Live sites in every project you can read, by project, with each site's visitors and page views over the range, the range before it and per hour (per day over 30d); the numbers are absent while the analytics store can't be reached. At most 200.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		Range string `query:"range" enum:"24h,7d,30d" default:"24h" doc:"How far back to count"`
	}) (*out[[]apitypes.OrgSite], error) {
		return reply(s.core.ListOrgSites(ctx, actor, in.Range))
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
		OperationID: "get-site-tracking", Method: http.MethodGet, Path: sitesPath + "/{site}/tracking",
		Summary: "Whether a site's tracking script works", Tags: tags,
		Description: "active when an event arrived in the last 24 hours; otherwise waiting, quiet or stopped from the last 30 days, or what the newest check of its pages found wrong (no_script, wrong_host, blocked). check_due asks for a check. Two store queries; nothing is fetched from the site.",
	}, func(ctx context.Context, actor core.Actor, in *SitePath) (*out[apitypes.SiteTracking], error) {
		return reply(s.core.SiteTracking(ctx, actor, in.Project, in.Site))
	})
	authed(s, a, huma.Operation{
		OperationID: "check-site-tracking", Method: http.MethodPost, Path: sitesPath + "/{site}/check",
		Summary: "Check a site's pages for its tracking script", Tags: tags,
		Description: "Fetches each of the site's hostnames' page (public addresses only, 5 seconds each), looks for the script tag and the Content-Security-Policy, and fetches /_dock/a/s.js to see that it is Dock's. At most once a minute per site; a newer check is returned as is. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, in *SitePath) (*out[apitypes.SiteTracking], error) {
		return reply(s.core.CheckSiteTracking(ctx, actor, in.Project, in.Site))
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
		Dimension []string `query:"dimension,explode" doc:"Break down by page, hostname, country, browser, browser_version, os, device, service or release; repeat for more"`
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
		OperationID: "get-site-public", Method: http.MethodGet, Path: sitesPath + "/{site}/public",
		Summary: "Where a site's public dashboard is, and whether it works yet", Tags: tags,
		Description: "Each address with the DNS records it needs and its status (DNS, then the certificate), checked now.",
	}, func(ctx context.Context, actor core.Actor, in *SitePath) (*out[apitypes.SitePublic], error) {
		return reply(s.core.SitePublic(ctx, actor, in.Project, in.Site))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-site-goals", Method: http.MethodGet, Path: sitesPath + "/{site}/goals",
		Summary: "A site's goals and their conversions", Tags: tags,
		Description: "Each goal with the visitors and sessions that reached it in the range (about 2% off), how often, and its share of the range's visitors. Takes the dashboard's filters.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		StatsParams
	}) (*out[apitypes.SiteGoals], error) {
		q, err := in.query()
		if err != nil {
			return nil, err
		}
		return reply(s.core.SiteGoals(ctx, actor, in.Project, in.Site, q))
	})
	authed(s, a, huma.Operation{
		OperationID: "add-site-goal", Method: http.MethodPost, Path: sitesPath + "/{site}/goals",
		Summary: "Add a goal to a site", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "A page (a path, * matching anything) or a custom event (its name). At most 20 per site. Needs deploy.",
		Errors:      []int{http.StatusConflict},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		Body apitypes.CreateGoalRequest
	}) (*out[apitypes.Goal], error) {
		return reply(s.core.AddSiteGoal(ctx, actor, in.Project, in.Site, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-site-goal", Method: http.MethodDelete, Path: sitesPath + "/{site}/goals/{goal}",
		Summary: "Remove a site's goal", Tags: tags, Description: "Needs deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		Goal string `path:"goal"`
	}) (*struct{}, error) {
		return nil, s.core.RemoveSiteGoal(ctx, actor, in.Project, in.Site, in.Goal)
	})
	authed(s, a, huma.Operation{
		OperationID: "list-site-funnels", Method: http.MethodGet, Path: sitesPath + "/{site}/funnels",
		Summary: "A site's funnels", Tags: tags,
	}, func(ctx context.Context, actor core.Actor, in *struct{ SitePath }) (*out[[]apitypes.Funnel], error) {
		return reply(s.core.SiteFunnels(ctx, actor, in.Project, in.Site))
	})
	authed(s, a, huma.Operation{
		OperationID: "add-site-funnel", Method: http.MethodPost, Path: sitesPath + "/{site}/funnels",
		Summary: "Add a funnel to a site", Tags: tags, DefaultStatus: http.StatusCreated,
		Description: "2 to 8 steps, each a page or a custom event, that a session takes in order. At most 20 per site. Needs deploy.",
		Errors:      []int{http.StatusConflict},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		Body apitypes.CreateFunnelRequest
	}) (*out[apitypes.Funnel], error) {
		return reply(s.core.AddSiteFunnel(ctx, actor, in.Project, in.Site, in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-site-funnel", Method: http.MethodGet, Path: sitesPath + "/{site}/funnels/{funnel}",
		Summary: "A funnel over a range", Tags: tags,
		Description: "How many sessions reached each step after the ones before it, and the share of the first step's. Takes the dashboard's filters.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		Funnel string `path:"funnel"`
		StatsParams
	}) (*out[apitypes.SiteFunnel], error) {
		q, err := in.query()
		if err != nil {
			return nil, err
		}
		return reply(s.core.SiteFunnel(ctx, actor, in.Project, in.Site, in.Funnel, q))
	})
	authed(s, a, huma.Operation{
		OperationID: "remove-site-funnel", Method: http.MethodDelete, Path: sitesPath + "/{site}/funnels/{funnel}",
		Summary: "Remove a site's funnel", Tags: tags, Description: "Needs deploy.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		Funnel string `path:"funnel"`
	}) (*struct{}, error) {
		return nil, s.core.RemoveSiteFunnel(ctx, actor, in.Project, in.Site, in.Funnel)
	})
	authed(s, a, huma.Operation{
		OperationID: "enable-service-analytics", Method: http.MethodPut, Path: servicePathT + "/analytics",
		Summary: "Count a service's visits", Tags: tags,
		Description: "Its domains that don't redirect count for its site (ADR 0104): the one it had before, with that history, else a new one named after it. Its pages then need <script src=\"/_dock/a/s.js\" defer></script>. The first site starts the analytics store. Needs deploy.",
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.Site], error) {
		return reply(s.core.EnableServiceAnalytics(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "disable-service-analytics", Method: http.MethodDelete, Path: servicePathT + "/analytics",
		Summary: "Stop counting a service's visits", Tags: tags,
		Description: "Its site is archived with its history, which comes back when counting is turned on again. Needs deploy.",
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*struct{}, error) {
		return nil, s.core.DisableServiceAnalytics(ctx, actor, in.ref())
	})
	authed(s, a, huma.Operation{
		OperationID: "move-site", Method: http.MethodPost, Path: sitesPath + "/{site}/move",
		Summary: "Move a site to another service", Tags: tags,
		Description: "Hands the site, with its history, settings, goals and funnels, to a service that doesn't count visits yet, such as one that replaced the site's own; the site's old service stops counting. Needs deploy on both environments.",
		Errors:      []int{http.StatusConflict},
	}, func(ctx context.Context, actor core.Actor, in *struct {
		SitePath
		Body apitypes.MoveSiteRequest
	}) (*out[apitypes.Site], error) {
		return reply(s.core.MoveSite(ctx, actor, in.Project, in.Site, in.Body))
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
	authed(s, a, huma.Operation{
		OperationID: "get-org-analytics-defaults", Method: http.MethodGet, Path: Prefix + "/org/analytics/defaults",
		Summary: "The organization's defaults for new sites", Tags: tags,
		Description: "What the organization set for its new analytics sites, and that over the platform's defaults. Scope: read.",
	}, func(ctx context.Context, actor core.Actor, _ *struct{}) (*out[apitypes.OrgSiteDefaults], error) {
		return reply(s.core.OrgSiteDefaults(ctx, actor))
	})
	authed(s, a, huma.Operation{
		OperationID: "set-org-analytics-defaults", Method: http.MethodPatch, Path: Prefix + "/org/analytics/defaults",
		Summary: "Change the organization's defaults for new sites", Tags: tags,
		Description: "New sites start with these settings; existing sites keep theirs. reset puts settings back to the platform's default. Scope: admin, unlimited.",
	}, func(ctx context.Context, actor core.Actor, in *struct{ Body apitypes.UpdateSiteRequest }) (*out[apitypes.OrgSiteDefaults], error) {
		return reply(s.core.SetOrgSiteDefaults(ctx, actor, in.Body.Settings, in.Body.Reset))
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
