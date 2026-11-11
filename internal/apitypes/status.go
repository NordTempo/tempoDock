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
	// Drafts: an uptime_down or crash_loop alert about a component's
	// sources opens a draft incident.
	Drafts *bool `json:"drafts,omitempty"`
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
	// ID keeps an existing component, with the incidents that name it;
	// without one a component of the same name is kept, else it is new.
	ID int64 `json:"id,omitempty"`
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
	Drafts     bool `json:"drafts"`
	// Status is the page's worst component (idle ones aside).
	Status     ComponentStatus   `json:"status" enum:"operational,idle,degraded,partial_outage,major_outage"`
	Components []StatusComponent `json:"components"`
	// Incidents are those open or resolved in the last StatusDays days,
	// newest first, drafts included.
	Incidents []StatusIncident `json:"incidents"`
}

// StatusComponent is a component with its sources and status.
type StatusComponent struct {
	ID          int64           `json:"id"`
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
	// Impact is the worst a published incident did to the component
	// that day, Incident that incident's title.
	Impact   ComponentStatus `json:"impact,omitempty" enum:"degraded,partial_outage,major_outage"`
	Incident string          `json:"incident,omitempty"`
}

// PublicStatusPage is what a published page shows anyone: names people
// chose, statuses and percentages. It holds nothing about services,
// checks, servers or errors, so nothing private can reach the page, its
// status.json or (later) its feed.
type PublicStatusPage struct {
	Title      string
	Status     ComponentStatus
	ShowUptime bool
	Groups     []PublicStatusGroup
	// Open are the published incidents not resolved yet, Recent those
	// resolved in the last StatusRecentDays days; newest first.
	Open, Recent []PublicIncident
	GeneratedAt  time.Time
}

// StatusRecentDays is how far back a page lists resolved incidents; its
// /history has the rest.
const StatusRecentDays = 14

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

// IncidentState is where an incident stands: its last update's state, or
// draft before the first. Drafts are never public.
type IncidentState string

// Incident states, in the order an incident usually goes through them.
const (
	IncidentDraft         IncidentState = "draft"
	IncidentInvestigating IncidentState = "investigating"
	IncidentIdentified    IncidentState = "identified"
	IncidentMonitoring    IncidentState = "monitoring"
	IncidentResolved      IncidentState = "resolved"
)

// IncidentStates are the states an update may post, for validation and
// help texts.
var IncidentStates = []IncidentState{IncidentInvestigating, IncidentIdentified, IncidentMonitoring, IncidentResolved}

// StatusImpact is what an incident does to one component: degraded,
// partial_outage or major_outage.
type StatusImpact struct {
	// Component is the component's public name on the incident's page.
	Component string          `json:"component" minLength:"1" maxLength:"60" example:"API"`
	Impact    ComponentStatus `json:"impact" enum:"degraded,partial_outage,major_outage"`
}

// OpenIncidentRequest opens an incident on a page: published at once
// with its first update, or as a draft for someone to publish.
type OpenIncidentRequest struct {
	Title      string         `json:"title" minLength:"1" maxLength:"120" example:"API errors for some requests"`
	Components []StatusImpact `json:"components,omitempty" maxItems:"100"`
	// State is the first update's: investigating (default), identified,
	// monitoring, or draft to keep it unpublished.
	State IncidentState `json:"state,omitempty" enum:"draft,investigating,identified,monitoring" doc:"The first update's state; draft keeps it unpublished. Default investigating"`
	// Message is the first update (a draft's words when State is draft).
	Message string `json:"message,omitempty" maxLength:"5000" doc:"Plain text; blank lines separate paragraphs and https links become links"`
}

// UpdateIncidentRequest changes an incident without posting an update:
// its title, the components it names (impact), or a draft's words.
type UpdateIncidentRequest struct {
	Title      *string         `json:"title,omitempty" minLength:"1" maxLength:"120"`
	Components *[]StatusImpact `json:"components,omitempty" maxItems:"100"`
	Draft      *string         `json:"draft,omitempty" maxLength:"5000" doc:"A draft's words, posted as its first update when it is published"`
}

// PostIncidentUpdateRequest posts an update: public at once. On a draft
// it publishes the incident.
type PostIncidentUpdateRequest struct {
	State   IncidentState `json:"state" enum:"investigating,identified,monitoring,resolved"`
	Message string        `json:"message" minLength:"1" maxLength:"5000" doc:"Plain text; blank lines separate paragraphs and https links become links"`
	// Components, when set, replaces the incident's impact with this one.
	Components *[]StatusImpact `json:"components,omitempty" maxItems:"100"`
}

// EditIncidentUpdateRequest fixes an update's words; the page says it
// was edited.
type EditIncidentUpdateRequest struct {
	Message string `json:"message" minLength:"1" maxLength:"5000"`
}

// StatusIncident is an incident as Dock's editor shows it.
type StatusIncident struct {
	// ID is the incident's public ID, in its public URL and the CLI.
	ID    string        `json:"id" example:"4f2kqz7mna"`
	Page  string        `json:"page"`
	Title string        `json:"title"`
	State IncidentState `json:"state" enum:"draft,investigating,identified,monitoring,resolved"`
	// Components are what it does to each component it names.
	Components []StatusImpact `json:"components"`
	// Draft is a draft's words.
	Draft       string     `json:"draft,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	// URL is its public page, "" while unpublished.
	URL       string `json:"url,omitempty"`
	CreatedBy string `json:"created_by"`
	// Alert is the alert Dock drafted it from (private).
	Alert   *IncidentAlert   `json:"alert,omitempty"`
	Updates []IncidentUpdate `json:"updates"`
}

// IncidentAlert is the alert an incident was drafted from.
type IncidentAlert struct {
	Kind    AlertKind `json:"kind"`
	Title   string    `json:"title"`
	Service string    `json:"service,omitempty" doc:"project/environment/service"`
}

// IncidentUpdate is an update as posted, oldest first in an incident.
type IncidentUpdate struct {
	ID       int64         `json:"id"`
	State    IncidentState `json:"state" enum:"investigating,identified,monitoring,resolved"`
	Message  string        `json:"message"`
	At       time.Time     `json:"at"`
	By       string        `json:"by"`
	EditedAt *time.Time    `json:"edited_at,omitempty"`
}

// PublicIncident is an incident on the public page: what people wrote,
// the components' public names and when. No authors, no alerts.
type PublicIncident struct {
	ID, Title  string
	State      IncidentState
	Components []StatusImpact
	StartedAt  time.Time
	ResolvedAt *time.Time
	// Updates are newest first.
	Updates []PublicUpdate
}

// PublicUpdate is a posted update on the public page.
type PublicUpdate struct {
	State   IncidentState
	Message string
	At      time.Time
	Edited  bool
}

// PublicStatusHistory is a page's published incidents, newest first, for
// its /history.
type PublicStatusHistory struct {
	Title     string
	Incidents []PublicIncident
	// More: older incidents exist beyond those listed.
	More bool
}

// PublicStatusFeed is a page's newest updates, for its Atom feed.
type PublicStatusFeed struct {
	Title   string
	Entries []PublicFeedEntry
	Updated time.Time
}

// PublicFeedEntry is one posted update with its incident.
type PublicFeedEntry struct {
	Incident, Title string
	PublicUpdate
}
