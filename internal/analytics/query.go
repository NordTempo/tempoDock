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
