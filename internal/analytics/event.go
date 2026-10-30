// Package analytics is Dock Analytics' shared vocabulary (ADR 0037): the
// events the ingest records, the store's interface and its HTTP client.
//
// Events flow one way: a browser on a site sends them to the ingest of the
// server that hosts the site, which appends them to its spool; the control
// plane forwards spooled events to the store, which runs as the
// dock-analytics container on the analytics server. Dashboards ask the
// store through the control plane.
//
// This package is pure Go and small: the dock binary links it, the
// dock-analytics program (its own module, CGO and DuckDB) imports it for
// the wire types.
package analytics

// Kind is what an event records.
type Kind uint8

const (
	// KindPageview is a page load or a single-page app's route change.
	KindPageview Kind = 1
	// KindEvent is a custom event: dock.track(name, props).
	KindEvent Kind = 2
	// KindOutbound is a click on a link to another site.
	KindOutbound Kind = 3
)

// Event is one thing a visitor did on a site, as the ingest records it and
// the store keeps it. JSON names are short: events are spooled and sent as
// JSON lines, and every byte is paid for per event.
//
// The ingest fills everything but the location and the session; the store
// looks the location up from IPPrefix (then drops it) and assigns the
// session.
type Event struct {
	// Time is when the ingest received the event, in Unix milliseconds.
	Time int64 `json:"t"`
	// Site, Service and Release are IDs from Dock's database: the site the
	// hostname belongs to, and the service and release that served it at
	// that moment.
	Site     int64  `json:"s"`
	Service  int64  `json:"sv"`
	Release  int64  `json:"rl"`
	Hostname string `json:"h"`
	Kind     Kind   `json:"k"`
	// Name is a custom event's name; for outbound clicks the link's URL.
	Name string `json:"n,omitempty"`
	// Path is the page's path, without the query.
	Path string `json:"p"`
	// Referrer is the referring site's hostname; empty for direct visits
	// and for navigation within the site.
	Referrer string `json:"r,omitempty"`
	// Channel is where the visit came from (direct, search, social, ...),
	// derived from the referrer and UTM parameters.
	Channel     string `json:"ch,omitempty"`
	UTMSource   string `json:"us,omitempty"`
	UTMMedium   string `json:"um,omitempty"`
	UTMCampaign string `json:"uc,omitempty"`
	UTMTerm     string `json:"ut,omitempty"`
	UTMContent  string `json:"ux,omitempty"`
	// Visitor is hash(daily salt, site, IP, user agent): the same person
	// on the same day, and nothing that names them (ADR 0037, cookieless).
	Visitor uint64 `json:"v"`
	// IPPrefix is the client's address cut to /24 (IPv6 /48), for the
	// location lookup only. The store never keeps it.
	IPPrefix       string `json:"ip,omitempty"`
	Browser        string `json:"b,omitempty"`
	BrowserVersion string `json:"bv,omitempty"`
	OS             string `json:"o,omitempty"`
	OSVersion      string `json:"ov,omitempty"`
	// Device is desktop, mobile or tablet.
	Device   string `json:"d,omitempty"`
	ScreenW  uint16 `json:"sw,omitempty"`
	ScreenH  uint16 `json:"sh,omitempty"`
	Language string `json:"l,omitempty"`
	// Props are a custom event's properties: a JSON object of string,
	// number and boolean values, at most MaxProps of them.
	Props string `json:"pr,omitempty"`
}

// MaxProps caps a custom event's properties.
const MaxProps = 30
