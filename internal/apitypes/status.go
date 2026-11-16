package apitypes

import "time"

// Public status pages (ADR 0073): an organization's pages of components,
// each built from services and uptime checks, served at
// status-<slug>.<base domain> once published.

// ComponentStatus is what a status page says about a component, or about
// the whole page (its worst component).
type ComponentStatus string

// Component statuses, from good to bad. Idle is a component whose
// environments are asleep (ADR 0070): neutral, never down. Maintenance is
// one a maintenance window in progress names.
const (
	StatusOperational   ComponentStatus = "operational"
	StatusIdle          ComponentStatus = "idle"
	StatusMaintenance   ComponentStatus = "maintenance"
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
	// Slug renames the page, and so moves its generated address to
	// status-<slug>.<base domain>. The old name keeps finding the page and
	// its address redirects to the page's until another page takes it.
	Slug  *string `json:"slug,omitempty" minLength:"1" maxLength:"32" example:"acme-api" doc:"A new name: lowercase letters, digits and dashes; the old address redirects"`
	Title *string `json:"title,omitempty" minLength:"1" maxLength:"80"`
	// Published serves the page at its address; unpublished, the address
	// answers 404.
	Published *bool `json:"published,omitempty"`
	// ShowUptime shows the percentages; the bars and statuses stay.
	ShowUptime *bool `json:"show_uptime,omitempty"`
	// Drafts: an uptime_down or crash_loop alert about a component's
	// sources opens a draft incident.
	Drafts *bool `json:"drafts,omitempty"`
	// Hostname is a custom hostname the page also answers on once it is
	// verified; "" removes it.
	Hostname *string `json:"hostname,omitempty" maxLength:"253" example:"status.acme.com" doc:"A custom hostname; empty removes it"`
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
	// URL is where it is served: its custom hostname once verified,
	// otherwise its generated address; "" while the control plane has no
	// base domain and the page no verified hostname.
	URL        string    `json:"url"`
	Components int       `json:"components"`
	CreatedAt  time.Time `json:"created_at"`
	CreatedBy  string    `json:"created_by"`
}

// StatusPage is a page with its components' live status and history, as
// its editor in Dock shows it.
type StatusPage struct {
	StatusPageSummary
	// BaseDomain is the control plane's, under which the page's generated
	// address is; "" without one.
	BaseDomain string `json:"base_domain,omitempty"`
	// Address is the page's generated address, status-<slug>-<random>.<base
	// domain> (ADR 0087; status-<slug> for a page from before that hasn't
	// been renamed), published or not; "" without a base domain.
	Address    string `json:"address,omitempty"`
	ShowUptime bool   `json:"show_uptime"`
	Drafts     bool   `json:"drafts"`
	// Status is the page's worst component (idle ones aside).
	Status     ComponentStatus   `json:"status" enum:"operational,idle,maintenance,degraded,partial_outage,major_outage"`
	Components []StatusComponent `json:"components"`
	// Incidents are those open or resolved in the last StatusDays days,
	// newest first, drafts included.
	Incidents []StatusIncident `json:"incidents"`
	// Hostname is the page's custom hostname, if it has one.
	Hostname *StatusHostname `json:"hostname,omitempty"`
}

// StatusHostname is a page's custom hostname (phase 4) and what it takes
// to serve it.
type StatusHostname struct {
	Hostname string `json:"hostname"`
	// Verified: the page answers on it (once published).
	Verified bool `json:"verified"`
	// Reason says what is missing while it isn't verified.
	Reason string `json:"reason,omitempty"`
	// URL is where the page is served on it, once verified.
	URL string `json:"url,omitempty"`
	// Records are the DNS records to add: the TXT that proves control
	// (until verified) and the CNAME to the page's own address. None
	// when Dock makes the record in Cloudflare (DNS).
	Records []DNSRecord `json:"records"`
	// DNS is the record Dock makes in a connected Cloudflare zone.
	DNS *DomainDNS `json:"dns,omitempty"`
}

// StatusComponent is a component with its sources and status.
type StatusComponent struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Group       string          `json:"group"`
	Description string          `json:"description"`
	Auto        bool            `json:"auto"`
	Status      ComponentStatus `json:"status" enum:"operational,idle,maintenance,degraded,partial_outage,major_outage"`
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
	// Maintenance: a maintenance window ran that day.
	Maintenance bool `json:"maintenance,omitempty"`
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
	// Open are the published incidents not resolved yet, Maintenance the
	// windows scheduled or in progress (soonest first), Recent the
	// incidents resolved and windows completed in the last
	// StatusRecentDays days, newest first.
	Open, Maintenance, Recent []PublicIncident
	GeneratedAt               time.Time
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

// IncidentStates are the states an update may post on an incident, for
// validation and help texts.
var IncidentStates = []IncidentState{IncidentInvestigating, IncidentIdentified, IncidentMonitoring, IncidentResolved}

// A maintenance window's states (phase 3): Dock moves it to in progress at
// its start and completes it at its end; people may start, complete or
// cancel it sooner.
const (
	MaintenanceScheduled  IncidentState = "scheduled"
	MaintenanceInProgress IncidentState = "in_progress"
	MaintenanceCompleted  IncidentState = "completed"
	MaintenanceCancelled  IncidentState = "cancelled"
)

// IncidentKind is what an incident is: an incident, or a maintenance
// window.
type IncidentKind string

const (
	KindIncident    IncidentKind = "incident"
	KindMaintenance IncidentKind = "maintenance"
)

// ScheduleMaintenanceRequest schedules a maintenance window over some of
// a page's components: public at once. While it is in progress its
// components read maintenance, their services' alerts are held and
// their uptime isn't counted.
type ScheduleMaintenanceRequest struct {
	Title string `json:"title" minLength:"1" maxLength:"120" example:"Database upgrade"`
	// Components are their public names.
	Components []string  `json:"components" minItems:"1" maxItems:"100" example:"[\"Database\"]"`
	StartsAt   time.Time `json:"starts_at" doc:"When it starts; one in the past starts now"`
	EndsAt     time.Time `json:"ends_at" doc:"When it is planned to end, at most 7 days after its start"`
	Message    string    `json:"message,omitempty" maxLength:"5000" doc:"What visitors read; plain text"`
}

// StatusImpact is what an incident does to one component: degraded,
// partial_outage or major_outage.
type StatusImpact struct {
	// Component is the component's public name on the incident's page.
	Component string          `json:"component" minLength:"1" maxLength:"60" example:"API"`
	Impact    ComponentStatus `json:"impact" enum:"degraded,partial_outage,major_outage,maintenance" doc:"maintenance only for a maintenance window's components"`
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
	// StartsAt moves a scheduled maintenance window; EndsAt extends or
	// shortens one not finished yet.
	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`
}

// PostIncidentUpdateRequest posts an update: public at once. On a draft
// it publishes the incident.
type PostIncidentUpdateRequest struct {
	// State is an incident's (investigating to resolved) or a maintenance
	// window's: scheduled or in_progress for a note, in_progress to start
	// it now, completed to end it now, cancelled.
	State   IncidentState `json:"state" enum:"investigating,identified,monitoring,resolved,scheduled,in_progress,completed,cancelled"`
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
	Kind  IncidentKind  `json:"kind" enum:"incident,maintenance"`
	Title string        `json:"title"`
	State IncidentState `json:"state" enum:"draft,investigating,identified,monitoring,resolved,scheduled,in_progress,completed,cancelled"`
	// Components are what it does to each component it names.
	Components []StatusImpact `json:"components"`
	// Draft is a draft's words.
	Draft string `json:"draft,omitempty"`
	// StartedAt is when it began, or a maintenance window's start; EndsAt
	// a window's planned end.
	StartedAt   time.Time  `json:"started_at"`
	EndsAt      *time.Time `json:"ends_at,omitempty"`
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
	State    IncidentState `json:"state" enum:"investigating,identified,monitoring,resolved,scheduled,in_progress,completed,cancelled"`
	Message  string        `json:"message"`
	At       time.Time     `json:"at"`
	By       string        `json:"by"`
	EditedAt *time.Time    `json:"edited_at,omitempty"`
}

// PublicIncident is an incident on the public page: what people wrote,
// the components' public names and when. No authors, no alerts.
type PublicIncident struct {
	ID, Title  string
	Kind       IncidentKind
	State      IncidentState
	Components []StatusImpact
	StartedAt  time.Time
	// EndsAt is a maintenance window's planned end.
	EndsAt     *time.Time
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

// StatusHookKind is where a status page hook posts (phase 5).
type StatusHookKind string

// Hook kinds: Slack and Discord incoming webhooks, and JSON webhooks
// signed like alert webhooks.
const (
	HookSlack   StatusHookKind = "slack"
	HookDiscord StatusHookKind = "discord"
	HookWebhook StatusHookKind = "webhook"
)

// AddStatusHookRequest adds a hook: Dock posts every public update of
// the page's incidents and maintenance there.
type AddStatusHookRequest struct {
	Name string         `json:"name" minLength:"1" maxLength:"40" example:"slack-status"`
	Kind StatusHookKind `json:"kind" enum:"slack,discord,webhook"`
	// URL is the incoming webhook's address; Dock keeps it sealed and
	// shows only its host.
	URL string `json:"url" minLength:"1" maxLength:"2000"`
	// Secret signs a webhook's posts; one is generated without it.
	Secret string `json:"secret,omitempty" maxLength:"200"`
}

// StatusHook is a page's hook as lists show it: never its URL's path or
// its secret.
type StatusHook struct {
	Name   string         `json:"name"`
	Kind   StatusHookKind `json:"kind" enum:"slack,discord,webhook"`
	Target string         `json:"target" doc:"The URL's scheme and host"`
	// LastError is the last failed delivery's, while it is newer than the
	// last good one.
	LastSentAt  *time.Time `json:"last_sent_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	CreatedBy   string     `json:"created_by"`
}

// CreatedStatusHook is a new hook, with the webhook secret Dock generated
// (shown only here).
type CreatedStatusHook struct {
	Hook   StatusHook `json:"hook"`
	Secret string     `json:"secret,omitempty"`
}

// StatusHookTest is how a test post went.
type StatusHookTest struct {
	OK    bool       `json:"ok"`
	Error string     `json:"error,omitempty"`
	Hook  StatusHook `json:"hook"`
}

// StatusHookPost is what a webhook hook receives: the page, the incident
// or maintenance window and the update just posted, as public as the
// page itself. Signed with the hook's secret in Dock-Signature, like
// alert webhooks. Event is "update" or "test".
type StatusHookPost struct {
	Event    string               `json:"event"`
	Page     StatusHookPage       `json:"page"`
	Incident StatusHookIncident   `json:"incident"`
	Update   StatusHookUpdateBody `json:"update"`
	SentAt   time.Time            `json:"sent_at"`
}

// StatusHookPage is the page a hook post is about.
type StatusHookPage struct {
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
}

// StatusHookIncident is the incident or window a hook post is about.
type StatusHookIncident struct {
	ID         string         `json:"id"`
	Kind       IncidentKind   `json:"kind"`
	Title      string         `json:"title"`
	State      IncidentState  `json:"state"`
	URL        string         `json:"url,omitempty"`
	Components []StatusImpact `json:"components"`
	StartsAt   time.Time      `json:"starts_at"`
	EndsAt     *time.Time     `json:"ends_at,omitempty"`
}

// StatusHookUpdateBody is the update a hook post carries.
type StatusHookUpdateBody struct {
	State   IncidentState `json:"state"`
	Message string        `json:"message"`
	At      time.Time     `json:"at"`
}
