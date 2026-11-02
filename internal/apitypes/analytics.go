package apitypes

import "time"

// AnalyticsStatus is Dock Analytics' state (ADR 0037), for operators:
// which server runs the store and whether it answers.
type AnalyticsStatus struct {
	// Enabled is set once analytics was turned on: the store runs and
	// every server's events are forwarded to it.
	Enabled bool `json:"enabled"`
	// Server runs the store: the control plane's own server by default.
	Server       string `json:"server"`
	ServerOnline bool   `json:"server_online"`
	// StoreRunning says the store answered just now; StoreVersion,
	// Events and StoreBytes are what it said.
	StoreRunning bool   `json:"store_running"`
	StoreVersion string `json:"store_version,omitempty"`
	Events       int64  `json:"events"`
	StoreBytes   int64  `json:"store_bytes"`
	// GeoIP is the location database's month, empty before the first
	// download.
	GeoIP string `json:"geoip,omitempty"`
	// Starting says the store is being started: its binary may be
	// downloading. Reading the status starts a store that should run
	// but doesn't answer.
	Starting bool `json:"starting,omitempty"`
	// MovingTo is the server the store is moving to, with its data,
	// while it moves.
	MovingTo string `json:"moving_to,omitempty"`
	// Error is why the store last failed to start, or a move failed, and
	// what to do; empty once it starts.
	Error string `json:"error,omitempty"`
}

// GeoAttribution credits the location data wherever locations appear
// (DB-IP's IP to City Lite, CC BY 4.0).
const GeoAttribution = "IP Geolocation by DB-IP"

// SetAnalyticsServerRequest picks the analytics server.
type SetAnalyticsServerRequest struct {
	// Server is a server's name; empty is the control plane's server.
	Server string `json:"server" doc:"The server the analytics store runs on; empty for the control plane's own"`
	// StartEmpty moves the store even though the old server is offline:
	// the new store starts empty, and the old one goes when its server
	// is back.
	StartEmpty bool `json:"start_empty,omitempty" doc:"Move even though the old analytics server is offline: the new store starts empty"`
}

// Site is a set of hostnames in one environment whose visits Dock
// Analytics counts together (ADR 0037). Its history follows a hostname
// when the hostname's domain moves to another service.
type Site struct {
	Name        string         `json:"name"`
	Environment string         `json:"environment"`
	Hostnames   []SiteHostname `json:"hostnames"`
	Archived    bool           `json:"archived"`
	CreatedAt   time.Time      `json:"created_at"`
	// Settings are in effect: the site's own, or the install's defaults.
	Settings SiteSettings `json:"settings"`
	// Overrides are the settings the site sets itself.
	Overrides SiteSettings `json:"overrides"`
}

// SiteHostname is one of a site's hostnames and the service it serves
// (empty while no service has a domain for it).
type SiteHostname struct {
	Hostname string `json:"hostname"`
	Service  string `json:"service,omitempty"`
}

// SiteSettings are a site's settings (ADR 0037). In Site.Settings every
// field is set; in overrides and in requests, nil means "the default".
type SiteSettings struct {
	Replay              *bool     `json:"replay,omitempty" doc:"Record sessions (phase A3); off by default"`
	ReplaySampleRate    *int      `json:"replay_sample_rate,omitempty" minimum:"1" maximum:"100" doc:"Percent of page loads recorded"`
	MaskAllText         *bool     `json:"mask_all_text,omitempty" doc:"Mask all text in replays; inputs are always masked"`
	ErrorsAndVitals     *bool     `json:"errors_and_vitals,omitempty" doc:"Collect errors and Web Vitals (phase A2)"`
	EventRetentionDays  *int      `json:"event_retention_days,omitempty" minimum:"1" maximum:"3650" doc:"Days raw events are kept; daily totals stay"`
	ReplayRetentionDays *int      `json:"replay_retention_days,omitempty" minimum:"1" maximum:"3650"`
	ErrorRetentionDays  *int      `json:"error_retention_days,omitempty" minimum:"1" maximum:"3650"`
	ExcludedPaths       *[]string `json:"excluded_paths,omitempty" maxItems:"50" doc:"Paths not counted; * matches any run of characters"`
	IgnoredIPs          *[]string `json:"ignored_ips,omitempty" maxItems:"50" doc:"Addresses or CIDR ranges whose visits aren't counted"`
	Public              *bool     `json:"public,omitempty" doc:"Public dashboard (phase A4)"`
}

// UpdateSiteRequest renames a site or changes its settings.
type UpdateSiteRequest struct {
	Name     string       `json:"name,omitempty" doc:"A new name"`
	Settings SiteSettings `json:"settings,omitzero" doc:"Settings to set on the site"`
	// Reset names settings (their JSON names) to set back to the default.
	Reset []string `json:"reset,omitempty"`
}

// EnableAnalyticsRequest turns analytics on for a hostname.
type EnableAnalyticsRequest struct {
	// Site joins a site of the same environment; empty joins the site of
	// the service's other hostnames, or makes one named after the
	// hostname.
	Site string `json:"site,omitempty"`
}

// SiteStats is a site's dashboard for a range: the numbers, the previous
// period's, the series and the top values of the dashboard's dimensions,
// and what happened (deploys, moves) for the chart's markers.
type SiteStats struct {
	Site     string            `json:"site"`
	From     int64             `json:"from" doc:"Unix ms"`
	To       int64             `json:"to" doc:"Unix ms"`
	Bucket   string            `json:"bucket" enum:"hour,day"`
	Overview AnalyticsOverview `json:"overview"`
	Previous AnalyticsOverview `json:"previous"`
	Series   []AnalyticsPoint  `json:"series"`
	// Breakdowns are keyed by dimension (page, referrer, country, ...).
	Breakdowns map[string][]AnalyticsRow `json:"breakdowns"`
	Markers    []SiteMarker              `json:"markers"`
	// StoreOnline is false while the analytics store can't be reached;
	// the numbers are then empty and events wait on each server.
	StoreOnline bool   `json:"store_online"`
	StoreError  string `json:"store_error,omitempty"`
	// Notice says what the numbers leave out: filters over a range
	// reaching past the site's event retention see only what is kept.
	Notice string `json:"notice,omitempty"`
}

// AnalyticsOverview is a range's headline numbers.
type AnalyticsOverview struct {
	Visitors   int64   `json:"visitors"`
	Pageviews  int64   `json:"pageviews"`
	Sessions   int64   `json:"sessions"`
	BounceRate float64 `json:"bounce_rate"`
	DurationMS int64   `json:"duration_ms"`
	Events     int64   `json:"events"`
}

// AnalyticsPoint is one bucket of a series; T is its start (Unix ms).
type AnalyticsPoint struct {
	T         int64 `json:"t"`
	Visitors  int64 `json:"visitors"`
	Pageviews int64 `json:"pageviews"`
}

// AnalyticsRow is one value of a breakdown. Value is what a filter on the
// dimension takes; Label names it where the value is an ID (services and
// releases: "web", "web r3").
type AnalyticsRow struct {
	Value    string `json:"value"`
	Label    string `json:"label,omitempty"`
	Visitors int64  `json:"visitors"`
	Count    int64  `json:"count"`
}

// SiteMarker is something that happened to a site, for its charts.
type SiteMarker struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind" enum:"deploy,added,moved,removed"`
	Message string    `json:"message"`
}

// SiteBreakdown is the top values of one dimension.
type SiteBreakdown struct {
	Dimension string         `json:"dimension"`
	Rows      []AnalyticsRow `json:"rows"`
}

// SiteLive is what a site's live view shows: visitors seen in the last
// five minutes and the latest events, newest first.
type SiteLive struct {
	Visitors int         `json:"visitors"`
	Recent   []LiveVisit `json:"recent"`
}

// LiveVisit is one recent event.
type LiveVisit struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind" enum:"pageview,event,outbound"`
	Path     string    `json:"path"`
	Name     string    `json:"name,omitempty"`
	Hostname string    `json:"hostname"`
	Referrer string    `json:"referrer,omitempty"`
	Country  string    `json:"country,omitempty"`
	City     string    `json:"city,omitempty"`
	Browser  string    `json:"browser,omitempty"`
	OS       string    `json:"os,omitempty"`
	Device   string    `json:"device,omitempty"`
}

// SiteErrors is a site's error groups seen in a range, the most frequent
// first (ADR 0037, phase A2).
type SiteErrors struct {
	Site   string       `json:"site"`
	From   int64        `json:"from" doc:"Unix ms"`
	To     int64        `json:"to" doc:"Unix ms"`
	Bucket string       `json:"bucket" enum:"hour,day"`
	Groups []ErrorGroup `json:"groups"`
	// Series is every error per bucket, Summary the range's totals and
	// Previous the period before's; Markers are the deploys and hostname
	// changes in the range.
	Series   []CountPoint `json:"series"`
	Summary  ErrorSummary `json:"summary"`
	Previous ErrorSummary `json:"previous"`
	Markers  []SiteMarker `json:"markers"`
	// Collecting is the site's errors_and_vitals setting: whether its
	// script reports errors and Web Vitals.
	Collecting  bool   `json:"collecting"`
	StoreOnline bool   `json:"store_online"`
	StoreError  string `json:"store_error,omitempty"`
}

// ErrorSummary is a range's errors in all: how many, the sessions and
// visitors (about 2% off) that met one, the groups they fall in and how
// many of those first appeared in the range.
type ErrorSummary struct {
	Errors    int64 `json:"errors"`
	Sessions  int64 `json:"sessions"`
	Visitors  int64 `json:"visitors"`
	Groups    int64 `json:"groups"`
	NewGroups int64 `json:"new_groups"`
	// SessionShare is Sessions over the range's sessions, 0 to 1.
	SessionShare float64 `json:"session_share"`
}

// ErrorGroup is errors with the same type, message and top frame. Counts,
// last seen and the message (the latest) are in the range; first seen
// and first release are the group's first error ever.
type ErrorGroup struct {
	Group   string `json:"group" doc:"The group's fingerprint, 16 hex digits"`
	Type    string `json:"type"`
	Message string `json:"message"`
	Frame   string `json:"frame" doc:"The top stack frame: function and script, without line or content hash"`
	Count   int64  `json:"count"`
	// Sessions and Visitors are how many met it at least once.
	Sessions  int64     `json:"sessions"`
	Visitors  int64     `json:"visitors"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// FirstRelease is the release that served the first one ("web r41").
	FirstRelease string       `json:"first_release,omitempty"`
	Series       []CountPoint `json:"series,omitempty"`
}

// CountPoint is one bucket of a count; T is its start (Unix ms).
type CountPoint struct {
	T     int64 `json:"t"`
	Count int64 `json:"count"`
}

// SiteError is one error group in a range: its latest stack, where it
// happened, and its latest occurrences.
type SiteError struct {
	Site   string `json:"site"`
	From   int64  `json:"from" doc:"Unix ms"`
	To     int64  `json:"to" doc:"Unix ms"`
	Bucket string `json:"bucket" enum:"hour,day"`
	ErrorGroup
	Stack    string            `json:"stack"`
	Pages    []AnalyticsRow    `json:"pages"`
	Browsers []AnalyticsRow    `json:"browsers"`
	OS       []AnalyticsRow    `json:"os"`
	Devices  []AnalyticsRow    `json:"devices"`
	Releases []AnalyticsRow    `json:"releases"`
	Recent   []ErrorOccurrence `json:"recent"`
}

// ErrorOccurrence is one error as a visitor met it.
type ErrorOccurrence struct {
	At             time.Time `json:"at"`
	Path           string    `json:"path"`
	Browser        string    `json:"browser,omitempty"`
	BrowserVersion string    `json:"browser_version,omitempty"`
	OS             string    `json:"os,omitempty"`
	Device         string    `json:"device,omitempty"`
	Country        string    `json:"country,omitempty"`
	Release        string    `json:"release,omitempty" doc:"The release that served the page (\"web r41\")"`
	// Session is the visit's ID, empty when the error came without a page
	// view to tie it to.
	Session string `json:"session,omitempty"`
	// Replay is a recording of the visit at the time, empty when it wasn't
	// recorded.
	Replay string `json:"replay,omitempty"`
}

// SiteVitals is a site's Web Vitals in a range: over all page loads, by
// the dimensions asked for, and one metric over time (ADR 0037, phase
// A2). A vital with no samples has p75 -1.
type SiteVitals struct {
	Site   string    `json:"site"`
	From   int64     `json:"from" doc:"Unix ms"`
	To     int64     `json:"to" doc:"Unix ms"`
	Bucket string    `json:"bucket" enum:"hour,day"`
	All    VitalsRow `json:"all"`
	// Breakdowns are keyed by dimension (page, device, ...), the most
	// page loads first.
	Breakdowns map[string][]VitalsRow `json:"breakdowns"`
	// Metric is the vital Series charts; Series has its buckets with
	// samples.
	Metric string       `json:"metric,omitempty" enum:"lcp,inp,cls,fcp,ttfb,"`
	Series []VitalPoint `json:"series"`
	// Markers are the deploys and hostname changes in the range.
	Markers []SiteMarker `json:"markers"`
	// Thresholds are each vital's good and poor limits, keyed lcp, inp,
	// cls, fcp, ttfb.
	Thresholds  map[string]VitalThreshold `json:"thresholds"`
	Collecting  bool                      `json:"collecting"`
	StoreOnline bool                      `json:"store_online"`
	StoreError  string                    `json:"store_error,omitempty"`
}

// VitalsRow is every vital over a set of page loads. Label names Value
// where it is an ID (services and releases).
type VitalsRow struct {
	Value string    `json:"value"`
	Label string    `json:"label,omitempty"`
	Loads int64     `json:"loads"`
	LCP   VitalStat `json:"lcp"`
	INP   VitalStat `json:"inp"`
	CLS   VitalStat `json:"cls"`
	FCP   VitalStat `json:"fcp"`
	TTFB  VitalStat `json:"ttfb"`
}

// VitalStat is one vital over some page loads: its 75th percentile, how
// many loads measured it, and (over all page loads, not in breakdowns)
// how many of those were good and poor.
type VitalStat struct {
	P75     float64 `json:"p75" doc:"Milliseconds; the layout shift score for CLS; -1 without samples"`
	Samples int64   `json:"samples"`
	Good    int64   `json:"good"`
	Poor    int64   `json:"poor"`
}

// VitalPoint is one vital's 75th percentile in a bucket; T is its start
// (Unix ms).
type VitalPoint struct {
	T       int64   `json:"t"`
	P75     float64 `json:"p75"`
	Samples int64   `json:"samples"`
}

// VitalThreshold is a vital's limits: good is at most Good, poor over Poor.
type VitalThreshold struct {
	Good float64 `json:"good"`
	Poor float64 `json:"poor"`
}

// SiteReplays is a site's session replays that began in a range, newest
// first (ADR 0037, phase A3).
type SiteReplays struct {
	Site    string   `json:"site"`
	From    int64    `json:"from" doc:"Unix ms"`
	To      int64    `json:"to" doc:"Unix ms"`
	Replays []Replay `json:"replays"`
	// Recording is the site's replay setting, and SampleRate the percent
	// of page loads recorded.
	Recording   bool   `json:"recording"`
	SampleRate  int    `json:"sample_rate"`
	StoreOnline bool   `json:"store_online"`
	StoreError  string `json:"store_error,omitempty"`
}

// Replay is a visitor's recorded page loads with no gap over 30 minutes,
// which play as one.
type Replay struct {
	ID        string    `json:"id" doc:"The replay's first page load, 16 hex digits"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Segments  int       `json:"segments" doc:"Page loads recorded"`
	EntryPath string    `json:"entry_path"`
	ExitPath  string    `json:"exit_path"`
	Country   string    `json:"country,omitempty"`
	Browser   string    `json:"browser,omitempty"`
	OS        string    `json:"os,omitempty"`
	Device    string    `json:"device,omitempty"`
	Release   string    `json:"release,omitempty" doc:"The release that served its first page (\"web r41\")"`
	Errors    int64     `json:"errors"`
	Events    int64     `json:"events" doc:"Custom events"`
	Bytes     int64     `json:"bytes"`
	// Marks are its page views and errors in order (at most 50), for a
	// list to draw its shape; lists only.
	Marks []ReplayMark `json:"marks,omitempty"`
}

// ReplayMark is a page view or an error during a replay.
type ReplayMark struct {
	T    int64  `json:"t" doc:"Unix ms"`
	Kind string `json:"kind" enum:"pageview,error"`
	// Name is an error's type.
	Name string `json:"name,omitempty"`
}

// ReplayDetail is one replay: its page loads, whose rrweb events are at
// .../replays/{replay}/segments/{segment}, and its timeline.
type ReplayDetail struct {
	Site string `json:"site"`
	Replay
	SegmentList []ReplaySegment `json:"segment_list"`
	Timeline    []TimelineItem  `json:"timeline"`
}

// ReplaySegment is one recorded page load. Add Skew to its rrweb events'
// times for the server's clock, which Start, End and the timeline use.
type ReplaySegment struct {
	ID     string    `json:"id"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Path   string    `json:"path"`
	SkewMS int64     `json:"skew_ms"`
	Bytes  int64     `json:"bytes"`
}

// TimelineItem is a page view, custom event or error during a replay.
type TimelineItem struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind" enum:"pageview,event,error"`
	Path string    `json:"path"`
	Name string    `json:"name,omitempty" doc:"An event's name, or an error's type and message"`
	// Group is an error's group, for its page.
	Group string `json:"group,omitempty"`
}

// GoalStep is what a goal or a funnel step matches: a page (a path,
// * matching any run of characters) or a custom event (its name).
type GoalStep struct {
	Kind  string `json:"kind" enum:"page,event"`
	Match string `json:"match" maxLength:"200" example:"/pricing"`
}

// CreateGoalRequest adds a goal to a site.
type CreateGoalRequest struct {
	Name string `json:"name" minLength:"1" maxLength:"60"`
	GoalStep
}

// Goal is a site's goal and, in a range, its conversions: the visitors
// and sessions that reached it (about 2% off) and how often it happened.
// Rate is Visitors over the range's visitors.
type Goal struct {
	Name string `json:"name"`
	GoalStep
	Visitors int64   `json:"visitors"`
	Sessions int64   `json:"sessions"`
	Count    int64   `json:"count"`
	Rate     float64 `json:"rate" doc:"0 to 1"`
}

// SiteGoals is a site's goals with their conversions in a range (ADR
// 0037, phase A4).
type SiteGoals struct {
	Site        string `json:"site"`
	From        int64  `json:"from" doc:"Unix ms"`
	To          int64  `json:"to" doc:"Unix ms"`
	Visitors    int64  `json:"visitors"`
	Goals       []Goal `json:"goals"`
	StoreOnline bool   `json:"store_online"`
	StoreError  string `json:"store_error,omitempty"`
}

// CreateFunnelRequest adds a funnel to a site: 2 to 8 steps a session
// takes in order.
type CreateFunnelRequest struct {
	Name  string     `json:"name" minLength:"1" maxLength:"60"`
	Steps []GoalStep `json:"steps" minItems:"2" maxItems:"8"`
}

// Funnel is a site's funnel.
type Funnel struct {
	Name  string     `json:"name"`
	Steps []GoalStep `json:"steps"`
}

// SiteFunnel is a funnel over a range: how many sessions reached each
// step after the ones before it.
type SiteFunnel struct {
	Site  string       `json:"site"`
	Name  string       `json:"name"`
	From  int64        `json:"from" doc:"Unix ms"`
	To    int64        `json:"to" doc:"Unix ms"`
	Steps []FunnelStep `json:"steps"`
	// StoreOnline is false while the store can't be reached; the counts
	// are then empty.
	StoreOnline bool   `json:"store_online"`
	StoreError  string `json:"store_error,omitempty"`
}

// FunnelStep is one step's sessions and visitors, and the share of the
// first step's sessions that got this far.
type FunnelStep struct {
	GoalStep
	Sessions   int64   `json:"sessions"`
	Visitors   int64   `json:"visitors"`
	Conversion float64 `json:"conversion" doc:"0 to 1, of the first step's sessions"`
}
