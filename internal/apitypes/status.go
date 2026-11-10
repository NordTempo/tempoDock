package apitypes

import "time"

// Public status pages (ADR 0073): an organization's pages of components,
// each built from services and uptime checks, served at
// status-<slug>.<base domain> once published.

// ComponentStatus is what a status page says about a component, or about
// the whole page (its worst component).
type ComponentStatus string

// Component statuses, from good to bad. Idle is a component whose
// environments are asleep (ADR 0070): neutral, never down.
const (
	StatusOperational   ComponentStatus = "operational"
	StatusIdle          ComponentStatus = "idle"
	StatusDegraded      ComponentStatus = "degraded"
	StatusPartialOutage ComponentStatus = "partial_outage"
	StatusMajorOutage   ComponentStatus = "major_outage"
)

// StatusDays is how many days of history a component shows, today
// included.
const StatusDays = 90

// CreateStatusPageRequest makes a status page, unpublished.
type CreateStatusPageRequest struct {
	// Slug names the page in the organization and is its address,
	// status-<slug>.<base domain>; one page's in the whole install.
	Slug string `json:"slug" minLength:"1" maxLength:"32" example:"acme" doc:"Lowercase letters, digits and dashes; the page's address is status-<slug>.<base domain>"`
	// Title is the page's heading. Default: the slug.
	Title string `json:"title,omitempty" maxLength:"80" example:"Acme"`
}

// UpdateStatusPageRequest changes a page's settings: any of its fields.
type UpdateStatusPageRequest struct {
	Title *string `json:"title,omitempty" minLength:"1" maxLength:"80"`
	// Published serves the page at its address; unpublished, the address
	// answers 404.
	Published *bool `json:"published,omitempty"`
	// ShowUptime shows the percentages; the bars and statuses stay.
	ShowUptime *bool `json:"show_uptime,omitempty"`
}

// StatusSourceRef names what a component is built from: a service, or one
// uptime check.
type StatusSourceRef struct {
	// Service is project/environment/service: all its uptime checks and
	// its health (crash loops, unhealthy).
	Service string `json:"service,omitempty" example:"shop/production/web" doc:"project/environment/service"`
	// Check is one uptime check's ID (dock uptime list).
	Check int64 `json:"check,omitempty" doc:"An uptime check's ID"`
}

// StatusComponentInput is one component in SetStatusComponentsRequest.
type StatusComponentInput struct {
	// Name is what the public sees: never a service's name unless you
	// choose it.
	Name        string `json:"name" minLength:"1" maxLength:"60" example:"API"`
	Group       string `json:"group,omitempty" maxLength:"40" example:"Core"`
	Description string `json:"description,omitempty" maxLength:"200"`
	// Auto: the status comes from the sources' checks and health. Off,
	// the component reads operational until incidents set it. Default on.
	Auto    *bool             `json:"auto,omitempty" doc:"Status from the sources' uptime checks and health; default true"`
	Sources []StatusSourceRef `json:"sources,omitempty" maxItems:"10"`
}

// SetStatusComponentsRequest is a page's whole component list, in order.
type SetStatusComponentsRequest struct {
	Components []StatusComponentInput `json:"components" maxItems:"100"`
}

// StatusPageSummary is a page in the organization's list.
type StatusPageSummary struct {
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	Published bool   `json:"published"`
	// URL is where it is served, "" while the control plane has no base
	// domain.
	URL        string    `json:"url"`
	Components int       `json:"components"`
	CreatedAt  time.Time `json:"created_at"`
	CreatedBy  string    `json:"created_by"`
}

// StatusPage is a page with its components' live status and history, as
// its editor in Dock shows it.
type StatusPage struct {
	StatusPageSummary
	ShowUptime bool `json:"show_uptime"`
	// Status is the page's worst component (idle ones aside).
	Status     ComponentStatus   `json:"status" enum:"operational,idle,degraded,partial_outage,major_outage"`
	Components []StatusComponent `json:"components"`
}

// StatusComponent is a component with its sources and status.
type StatusComponent struct {
	Name        string          `json:"name"`
	Group       string          `json:"group"`
	Description string          `json:"description"`
	Auto        bool            `json:"auto"`
	Status      ComponentStatus `json:"status" enum:"operational,idle,degraded,partial_outage,major_outage"`
	Sources     []StatusSource  `json:"sources"`
	// Uptime is over the days shown, nil without probes.
	Uptime *float64 `json:"uptime,omitempty"`
	// Days are the last StatusDays UTC days, oldest first.
	Days []StatusDay `json:"days"`
}

// StatusSource is a component's source as its editor shows it.
type StatusSource struct {
	StatusSourceRef
	// Hidden: it is in a project you can't see, so it isn't named.
	Hidden bool `json:"hidden,omitempty"`
	// Checks counts the uptime checks it covers.
	Checks int `json:"checks"`
	// Problem says why it adds nothing now: its service is archived, or
	// its environment asleep.
	Problem string `json:"problem,omitempty"`
}

// StatusDay is one UTC day of a component's history.
type StatusDay struct {
	Date string `json:"date" example:"2026-11-10"`
	// Uptime is the share of probes that were up, nil without probes.
	Uptime *float64 `json:"uptime,omitempty"`
}

// PublicStatusPage is what a published page shows anyone: names people
// chose, statuses and percentages. It holds nothing about services,
// checks, servers or errors, so nothing private can reach the page, its
// status.json or (later) its feed.
type PublicStatusPage struct {
	Title       string
	Status      ComponentStatus
	ShowUptime  bool
	Groups      []PublicStatusGroup
	GeneratedAt time.Time
}

// PublicStatusGroup is a run of a page's components under one group name
// ("" for those without one).
type PublicStatusGroup struct {
	Name       string
	Components []PublicStatusComponent
}

// PublicStatusComponent is a component on the public page. Uptime is nil
// without probes or when the page hides percentages.
type PublicStatusComponent struct {
	Name, Description string
	Status            ComponentStatus
	Uptime            *float64
	Days              []StatusDay
}

// StatusSourceCandidate is a service a component may be built from, with
// its uptime checks.
type StatusSourceCandidate struct {
	// Service is project/environment/service.
	Service string                 `json:"service"`
	Checks  []StatusCheckCandidate `json:"checks"`
}

// StatusCheckCandidate is an uptime check a component may be built from.
type StatusCheckCandidate struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}
