package apitypes

import "time"

// Uptime checks (ADR 0055): a URL on one of a service's domains, probed
// from another server.

// UptimeState is what a check's latest probes say.
type UptimeState string

// Uptime states. A check is down after two failed probes in a row, up
// after a successful one; unknown until then.
const (
	UptimeUp      UptimeState = "up"
	UptimeDown    UptimeState = "down"
	UptimeUnknown UptimeState = "unknown"
)

// AddUptimeCheckRequest adds an uptime check to a service.
type AddUptimeCheckRequest struct {
	// URL is http:// or https:// on one of the service's domains, any
	// path.
	URL string `json:"url" minLength:"1" maxLength:"2048" example:"https://shop.example.com/health"`
	// ExpectStatus is the status that means up: a code, or 2xx or 3xx for
	// any code of the class. Default 200.
	ExpectStatus string `json:"expect_status,omitempty" pattern:"^([1-5][0-9][0-9]|2xx|3xx)$" doc:"A status code, or 2xx or 3xx; default 200"`
	// IntervalSeconds is how often it is probed: 30 to 3600, default 60.
	IntervalSeconds int `json:"interval_seconds,omitempty" minimum:"0" maximum:"3600" doc:"30 to 3600; default 60"`
	// TimeoutSeconds bounds a probe: 1 to 30 and below the interval,
	// default 10.
	TimeoutSeconds int `json:"timeout_seconds,omitempty" minimum:"0" maximum:"30" doc:"1 to 30, below the interval; default 10"`
}

// UptimeCheck is a check with its latest results.
type UptimeCheck struct {
	ID              int64     `json:"id"`
	URL             string    `json:"url"`
	ExpectStatus    string    `json:"expect_status" example:"200"`
	IntervalSeconds int       `json:"interval_seconds"`
	TimeoutSeconds  int       `json:"timeout_seconds"`
	CreatedAt       time.Time `json:"created_at"`
	CreatedBy       string    `json:"created_by"`
	// Auto: Dock added it for a verified domain (ADR 0059). It goes when
	// the domain does; a user may remove it like any other.
	Auto bool `json:"auto"`
	// State is unknown until the first probes after the daemon started.
	State UptimeState `json:"state" enum:"up,down,unknown"`
	// Server probes it now: another server than the service's when one
	// is online, else the control plane's.
	Server string `json:"server"`
	// LastProbe is the latest probe since the daemon started.
	LastProbe *UptimeProbe `json:"last_probe,omitempty"`
	// Uptime is the percentage of probes that were up over 24 hours, 7
	// days and 30 days; absent for a window without probes.
	Uptime24h *float64 `json:"uptime_24h,omitempty"`
	Uptime7d  *float64 `json:"uptime_7d,omitempty"`
	Uptime30d *float64 `json:"uptime_30d,omitempty"`
	// Series is the last 24 hours in 30-minute buckets, oldest first:
	// always 48, a bucket without probes has total 0.
	Series []UptimeBucket `json:"series"`
	// Incidents are the check's newest uptime_down alerts, newest first
	// (at most 5).
	Incidents []UptimeIncident `json:"incidents"`
}

// UptimeProbe is one probe's result.
type UptimeProbe struct {
	At time.Time `json:"at"`
	Up bool      `json:"up"`
	// Status is the answer's code; 0 when none came.
	Status    int    `json:"status,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
	// Server is the server that probed.
	Server string `json:"server"`
}

// UptimeBucket is a check's probes in one interval.
type UptimeBucket struct {
	At    time.Time `json:"at"`
	Up    int       `json:"up"`
	Total int       `json:"total"`
}

// UptimeIncident is a time a check was down: its alert.
type UptimeIncident struct {
	AlertID    int64      `json:"alert_id"`
	Message    string     `json:"message,omitempty"`
	OpenedAt   time.Time  `json:"opened_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// ServiceUptime sums up a service's uptime checks in overviews.
type ServiceUptime struct {
	// Checks is how many checks the service has.
	Checks int `json:"checks"`
	// State is down when any check is down, up when every check that has
	// been probed is up, unknown otherwise.
	State UptimeState `json:"state" enum:"up,down,unknown"`
	// Uptime24h is the percentage of the checks' probes that were up in
	// the last 24 hours; absent without probes.
	Uptime24h *float64 `json:"uptime_24h,omitempty"`
}

// UptimeUpdate is a check whose state changed (up or down), in an
// uptime event, which names its project, environment and service.
type UptimeUpdate struct {
	ID        int64        `json:"id"`
	State     UptimeState  `json:"state" enum:"up,down,unknown"`
	LastProbe *UptimeProbe `json:"last_probe,omitempty"`
}
