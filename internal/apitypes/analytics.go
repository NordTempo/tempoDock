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
	// Collecting is the site's errors_and_vitals setting: whether its
	// script reports errors and Web Vitals.
	Collecting  bool   `json:"collecting"`
	StoreOnline bool   `json:"store_online"`
	StoreError  string `json:"store_error,omitempty"`
}

// ErrorGroup is errors with the same type, message and top frame. Counts
// are in the range; first and last seen are over the site's history.
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
}

// SiteVitals is a site's Web Vitals in a range: over all page loads, by
// one dimension and over time (ADR 0037, phase A2). A vital with no
// samples has p75 -1.
type SiteVitals struct {
	Site      string        `json:"site"`
	From      int64         `json:"from" doc:"Unix ms"`
	To        int64         `json:"to" doc:"Unix ms"`
	Bucket    string        `json:"bucket" enum:"hour,day"`
	Dimension string        `json:"dimension,omitempty"`
	All       VitalsRow     `json:"all"`
	Rows      []VitalsRow   `json:"rows"`
	Series    []VitalsPoint `json:"series"`
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
// many loads measured it, and how many of those were good and poor.
type VitalStat struct {
	P75     float64 `json:"p75" doc:"Milliseconds; the layout shift score for CLS; -1 without samples"`
	Samples int64   `json:"samples"`
	Good    int64   `json:"good"`
	Poor    int64   `json:"poor"`
}

// VitalsPoint is every vital's 75th percentile in one bucket (-1: none).
type VitalsPoint struct {
	T    int64   `json:"t"`
	LCP  float64 `json:"lcp"`
	INP  float64 `json:"inp"`
	CLS  float64 `json:"cls"`
	FCP  float64 `json:"fcp"`
	TTFB float64 `json:"ttfb"`
}

// VitalThreshold is a vital's limits: good is at most Good, poor over Poor.
type VitalThreshold struct {
	Good float64 `json:"good"`
	Poor float64 `json:"poor"`
}
