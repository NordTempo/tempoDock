package apitypes

import "time"

// Error issues (ADR 0062, "One place for errors"): errors grouped by
// fingerprint from three sources, per service, for an org's members.

// ErrorSource is where an issue's errors come from.
type ErrorSource string

// Error sources.
const (
	// ErrorFromFrontend: browser errors Dock Analytics collected on the
	// service's site (ADR 0037).
	ErrorFromFrontend ErrorSource = "frontend"
	// ErrorFromLogs: errors the service's containers logged (a stated
	// error level, a stack trace, a panic), read from their output as it
	// is written.
	ErrorFromLogs ErrorSource = "logs"
	// ErrorFromTraffic: a spike of 5xx answers from the service.
	ErrorFromTraffic ErrorSource = "traffic"
)

// ErrorState is where an issue stands.
type ErrorState string

// Error states, the order an issue list puts them in.
const (
	// ErrorNew: first seen in the service's active release, or in the
	// last 24 hours.
	ErrorNew ErrorState = "new"
	// ErrorRegressed: seen again after it was resolved (by a person, or
	// by itself after 7 days without one).
	ErrorRegressed ErrorState = "regressed"
	// ErrorOngoing: seen before this release and still seen.
	ErrorOngoing ErrorState = "ongoing"
	// ErrorResolved: resolved and not seen since.
	ErrorResolved ErrorState = "resolved"
)

// ErrorIssue is one group of errors with the same fingerprint in one
// service.
type ErrorIssue struct {
	ID          int64       `json:"id"`
	Fingerprint string      `json:"fingerprint" doc:"Stable within the service: the error's type, its message without numbers and IDs, and its top stack frames, hashed"`
	Source      ErrorSource `json:"source" enum:"frontend,logs,traffic"`
	State       ErrorState  `json:"state" enum:"new,regressed,ongoing,resolved"`
	// Title is the error's type and message ("TypeError: Cannot read
	// properties of undefined"); Culprit where it happened: the top frame
	// of its stack ("Cart.total (src/cart.ts:42)") or a route ("POST
	// /api/checkout").
	Title       string `json:"title"`
	Culprit     string `json:"culprit,omitempty"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	// Count is every error of the issue Dock has counted; Trend the last
	// 24 hours' counts, an hour each, oldest first.
	Count     int64     `json:"count"`
	Trend     []int64   `json:"trend"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// FirstRelease and LastRelease are the service's releases it was
	// first and last seen in; 0 when not known.
	FirstRelease int64      `json:"first_release,omitempty"`
	LastRelease  int64      `json:"last_release,omitempty"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy   string     `json:"resolved_by,omitempty"`
}

// ErrorIssueDetail is an issue with its latest sample: a stack trace, a
// log excerpt or the 5xx counts, at most 8 KB.
type ErrorIssueDetail struct {
	ErrorIssue
	Sample string `json:"sample"`
	// LatestTrace is the newest trace with this error (ADR 0075): a span
	// whose exception has the issue's fingerprint, around the last time
	// it was seen. Set for log errors while tracing is on.
	LatestTrace *TraceRef `json:"latest_trace,omitempty" doc:"The newest trace with this error, while tracing is on"`
}

// ErrorFacet counts the open issues (not resolved) of one service, for
// the errors page's tree of projects and services.
type ErrorFacet struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	Issues      int    `json:"issues"`
	// New counts issues that are new or regressed; Events the errors of
	// all its issues in the last 24 hours.
	New    int   `json:"new"`
	Events int64 `json:"events"`
}

// ErrorList is the errors page: the issues that match its filters, new
// and regressed first, then by last seen; and the facets of every
// service in the org's matching environment, so the tree shows where
// errors are whatever service is picked.
type ErrorList struct {
	Issues []ErrorIssue `json:"issues"`
	Facets []ErrorFacet `json:"facets"`
	// Truncated says there were more issues than the list holds.
	Truncated bool `json:"truncated,omitempty"`
}
