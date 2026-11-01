package analytics

// Dimension is something events can be broken down and filtered by.
type Dimension string

// Dimensions. Referrer, channel and the UTM parameters belong to the
// session: every event of a session carries those of its first event.
const (
	DimPage           Dimension = "page"
	DimEntryPage      Dimension = "entry_page"
	DimExitPage       Dimension = "exit_page"
	DimHostname       Dimension = "hostname"
	DimReferrer       Dimension = "referrer"
	DimChannel        Dimension = "channel"
	DimUTMSource      Dimension = "utm_source"
	DimUTMMedium      Dimension = "utm_medium"
	DimUTMCampaign    Dimension = "utm_campaign"
	DimUTMTerm        Dimension = "utm_term"
	DimUTMContent     Dimension = "utm_content"
	DimCountry        Dimension = "country"
	DimRegion         Dimension = "region"
	DimCity           Dimension = "city"
	DimBrowser        Dimension = "browser"
	DimBrowserVersion Dimension = "browser_version"
	DimOS             Dimension = "os"
	DimOSVersion      Dimension = "os_version"
	DimDevice         Dimension = "device"
	DimScreen         Dimension = "screen"
	DimLanguage       Dimension = "language"
	DimEvent          Dimension = "event"
	DimOutbound       Dimension = "outbound"
	DimService        Dimension = "service"
	DimRelease        Dimension = "release"
)

// Dimensions lists every Dimension, in the order dashboards show them.
var Dimensions = []Dimension{
	DimPage, DimEntryPage, DimExitPage, DimHostname, DimReferrer, DimChannel,
	DimUTMSource, DimUTMMedium, DimUTMCampaign, DimUTMTerm, DimUTMContent,
	DimCountry, DimRegion, DimCity, DimBrowser, DimBrowserVersion, DimOS, DimOSVersion,
	DimDevice, DimScreen, DimLanguage, DimEvent, DimOutbound, DimService, DimRelease,
}

// FilterOp is how a Filter compares.
type FilterOp string

const (
	OpIs       FilterOp = "is"
	OpIsNot    FilterOp = "is_not"
	OpContains FilterOp = "contains"
)

// Filter keeps the events whose Dimension matches Value.
type Filter struct {
	Dimension Dimension `json:"dimension"`
	Op        FilterOp  `json:"op"`
	Value     string    `json:"value"`
}

// MaxFilters caps a query's filters.
const MaxFilters = 10

// Query selects a site's events in [From, To) (Unix ms) that match every
// filter.
type Query struct {
	Site    int64    `json:"site"`
	From    int64    `json:"from"`
	To      int64    `json:"to"`
	Filters []Filter `json:"filters,omitempty"`
}

// Overview is a query's headline numbers.
type Overview struct {
	Visitors  int64 `json:"visitors"`
	Pageviews int64 `json:"pageviews"`
	Sessions  int64 `json:"sessions"`
	// BounceRate is the share of sessions with at most one page view, 0
	// to 1.
	BounceRate float64 `json:"bounce_rate"`
	// Duration is the average session's length in milliseconds, from its
	// first to its last event.
	Duration int64 `json:"duration_ms"`
	// Events are custom events and outbound clicks.
	Events int64 `json:"events"`
}

// Bucket is a Series' point width.
type Bucket string

const (
	BucketMinute Bucket = "minute"
	BucketHour   Bucket = "hour"
	BucketDay    Bucket = "day"
)

// SeriesQuery asks for visitors and page views over time.
type SeriesQuery struct {
	Query
	Bucket Bucket `json:"bucket"`
}

// Point is one bucket of a series; T is its start (Unix ms).
type Point struct {
	T         int64 `json:"t"`
	Visitors  int64 `json:"visitors"`
	Pageviews int64 `json:"pageviews"`
}

// BreakdownQuery asks for the top values of one dimension.
type BreakdownQuery struct {
	Query
	Dimension Dimension `json:"dimension"`
	Limit     int       `json:"limit"`
}

// MaxBreakdown caps a breakdown's rows.
const MaxBreakdown = 1000

// Row is one value of a breakdown. Count is page views for pages, events
// for custom events and outbound links, sessions for entry and exit
// pages, and events for the rest.
type Row struct {
	Value    string `json:"value"`
	Visitors int64  `json:"visitors"`
	Count    int64  `json:"count"`
}

// LiveQuery asks for a site's visitors since Since (Unix ms) and its
// latest events: what a live view starts from.
type LiveQuery struct {
	Site  int64 `json:"site"`
	Since int64 `json:"since"`
}

// MaxLiveVisitors caps Live's visitors.
const MaxLiveVisitors = 10_000

// Live is a site's recent activity.
type Live struct {
	// Visitors are those seen since the query's Since, with when each was
	// last seen (Unix ms); at most MaxLiveVisitors.
	Visitors []LiveVisitor `json:"visitors"`
	// Recent are the latest events, newest first, at most 20.
	Recent []Event `json:"recent"`
}

// LiveVisitor is a visitor and when they were last seen.
type LiveVisitor struct {
	Visitor uint64 `json:"visitor"`
	Last    int64  `json:"last"`
}

// ErrorDimensions are the dimensions error and vitals queries filter and
// break down by: what an error or a page load's vitals record of their
// page and visitor.
var ErrorDimensions = []Dimension{
	DimPage, DimHostname, DimCountry, DimBrowser, DimBrowserVersion, DimOS, DimDevice, DimService, DimRelease,
}

// ErrorsQuery asks for a site's error groups seen in a range, the most
// frequent first, each with its series in Bucket (none if empty).
type ErrorsQuery struct {
	Query
	Bucket Bucket `json:"bucket,omitempty"`
	Limit  int    `json:"limit"`
}

// MaxErrorGroups caps an ErrorsQuery's groups.
const MaxErrorGroups = 200

// ErrorGroup is errors with the same fingerprint: type, message and top
// frame. Counts are in the query's range; FirstSeen and LastSeen are
// over the site's whole history (Unix ms).
type ErrorGroup struct {
	// Group is the fingerprint in hex: JSON numbers can't hold 64 bits.
	Group   string `json:"group"`
	Type    string `json:"type"`
	Message string `json:"message"`
	Frame   string `json:"frame"`
	Count   int64  `json:"count"`
	// Sessions and Visitors are how many saw it at least once.
	Sessions  int64 `json:"sessions"`
	Visitors  int64 `json:"visitors"`
	FirstSeen int64 `json:"first_seen"`
	LastSeen  int64 `json:"last_seen"`
	// FirstRelease is the release that served the first one.
	FirstRelease int64        `json:"first_release"`
	Series       []CountPoint `json:"series,omitempty"`
}

// CountPoint is one bucket of a count over time; T is its start (Unix ms).
type CountPoint struct {
	T     int64 `json:"t"`
	Count int64 `json:"count"`
}

// ErrorQuery asks for one error group in a range.
type ErrorQuery struct {
	Query
	Group  string `json:"group"`
	Bucket Bucket `json:"bucket,omitempty"`
}

// ErrorDetail is one error group: its latest stack, where it happened and
// its latest occurrences. Breakdown rows count errors and visitors.
type ErrorDetail struct {
	ErrorGroup
	Stack    string            `json:"stack"`
	Pages    []Row             `json:"pages"`
	Browsers []Row             `json:"browsers"`
	OS       []Row             `json:"os"`
	Devices  []Row             `json:"devices"`
	Releases []Row             `json:"releases"`
	Recent   []ErrorOccurrence `json:"recent"`
}

// MaxErrorRecent caps an ErrorDetail's occurrences.
const MaxErrorRecent = 50

// ErrorOccurrence is one error as a visitor met it.
type ErrorOccurrence struct {
	Time           int64  `json:"t"`
	Path           string `json:"path"`
	Browser        string `json:"browser"`
	BrowserVersion string `json:"browser_version"`
	OS             string `json:"os"`
	Device         string `json:"device"`
	Country        string `json:"country"`
	Service        int64  `json:"service"`
	Release        int64  `json:"release"`
	// Session is the session's ID in hex, "" when the error came without
	// a page view to tie it to.
	Session string `json:"session"`
}

// Vital names a Web Vital.
type Vital string

// The Web Vitals, in the order dashboards show them.
const (
	VitalLCP  Vital = "lcp"
	VitalINP  Vital = "inp"
	VitalCLS  Vital = "cls"
	VitalFCP  Vital = "fcp"
	VitalTTFB Vital = "ttfb"
)

// VitalNames lists every Vital.
var VitalNames = []Vital{VitalLCP, VitalINP, VitalCLS, VitalFCP, VitalTTFB}

// VitalThreshold is where a vital stops being good and where it becomes
// poor (Google's thresholds, at the 75th percentile): good is at most
// Good, poor is over Poor.
type VitalThreshold struct {
	Good, Poor float64
}

// VitalThresholds are each vital's thresholds.
var VitalThresholds = map[Vital]VitalThreshold{
	VitalLCP: {2500, 4000}, VitalINP: {200, 500}, VitalCLS: {0.1, 0.25}, VitalFCP: {1800, 3000}, VitalTTFB: {800, 1800},
}

// VitalsQuery asks for a site's Web Vitals in a range: over all page
// loads, by Dimension's values (none if empty) and over time in Bucket
// (none if empty).
type VitalsQuery struct {
	Query
	Dimension Dimension `json:"dimension,omitempty"`
	Limit     int       `json:"limit,omitempty"`
	Bucket    Bucket    `json:"bucket,omitempty"`
}

// VitalStat is one vital over some page loads: its 75th percentile (-1
// with no samples), how many loads measured it, and how many of those
// were good and poor.
type VitalStat struct {
	P75     float64 `json:"p75"`
	Samples int64   `json:"samples"`
	Good    int64   `json:"good"`
	Poor    int64   `json:"poor"`
}

// VitalsRow is every vital over a set of page loads: all of them, or one
// value's.
type VitalsRow struct {
	Value string    `json:"value"`
	Loads int64     `json:"loads"`
	LCP   VitalStat `json:"lcp"`
	INP   VitalStat `json:"inp"`
	CLS   VitalStat `json:"cls"`
	FCP   VitalStat `json:"fcp"`
	TTFB  VitalStat `json:"ttfb"`
}

// VitalsPoint is every vital's 75th percentile in one bucket (-1 for
// none); T is its start (Unix ms).
type VitalsPoint struct {
	T    int64   `json:"t"`
	LCP  float64 `json:"lcp"`
	INP  float64 `json:"inp"`
	CLS  float64 `json:"cls"`
	FCP  float64 `json:"fcp"`
	TTFB float64 `json:"ttfb"`
}

// VitalsResult answers a VitalsQuery.
type VitalsResult struct {
	All    VitalsRow     `json:"all"`
	Rows   []VitalsRow   `json:"rows"`
	Series []VitalsPoint `json:"series"`
}
