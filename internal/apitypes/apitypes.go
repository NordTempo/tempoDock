// Package apitypes is the JSON contract of the platform API (/api/v1).
//
// It has no server dependencies so the CLI can import it directly (ADR 0005).
// The daemon's core returns these types too, so the API, CLI, UI and core all
// use the same words. JSON field names are snake_case and stable: agents and
// scripts depend on them. Struct tags like `doc`, `enum` and `pattern` feed
// the generated OpenAPI spec.
package apitypes

import "time"

// APIVersion is the version segment of every API path.
const APIVersion = "v1"

// Scope is a permission level. Each scope includes the ones below it:
// read < deploy < admin.
type Scope string

const (
	// ScopeRead allows status, logs, releases and variable names.
	ScopeRead Scope = "read"
	// ScopeDeploy allows deploys, rollbacks, restarts, variables and domains.
	ScopeDeploy Scope = "deploy"
	// ScopeAdmin allows archiving, secret values, users and tokens.
	ScopeAdmin Scope = "admin"
)

// Version identifies the daemon, for CLI compatibility checks.
type Version struct {
	Version    string `json:"version" example:"v0.1.0"`
	APIVersion string `json:"api_version" example:"v1"`
	// MaxUploadBytes caps source uploads (deploy-source); clients can warn
	// before sending a bigger build context.
	MaxUploadBytes int64 `json:"max_upload_bytes"`
}

// Health reports that the daemon is up.
type Health struct {
	Status  string `json:"status" example:"ok"`
	Version string `json:"version" example:"v0.1.0"`
}

// Me describes the caller: a logged-in user or an API token.
type Me struct {
	Kind  string `json:"kind" enum:"user,token"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	Scope Scope  `json:"scope" enum:"read,deploy,admin"`
	// Project and Environment are set when a token is limited to them.
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// LoginRequest starts a UI session.
type LoginRequest struct {
	Email    string `json:"email" format:"email"`
	Password string `json:"password" minLength:"1"`
}

// Project groups environments.
type Project struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// ArchivedAt is set for archived projects, which are hidden from lists
	// unless asked for.
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
}

// CreateProjectRequest creates a project.
type CreateProjectRequest struct {
	Name string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"DNS-safe name"`
}

// Environment is a set of services placed together on one server.
type Environment struct {
	ID        int64     `json:"id"`
	Project   string    `json:"project"`
	Name      string    `json:"name"`
	Server    string    `json:"server" doc:"Server the environment runs on"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateEnvironmentRequest creates an environment. Environments named
// `production` go to the production server, others to a non-production one.
type CreateEnvironmentRequest struct {
	Name string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"DNS-safe name"`
}

// ServiceState is the runtime state of a service's active container.
type ServiceState string

// Service states.
const (
	ServiceRunning    ServiceState = "running"
	ServiceRestarting ServiceState = "restarting"
	ServiceCrashed    ServiceState = "crashed"
	ServiceStopped    ServiceState = "stopped"
	// ServiceNone means there is no active release (never deployed or archived).
	ServiceNone ServiceState = "none"
)

// ServiceStatus is the observed runtime status of a service.
type ServiceStatus struct {
	State        ServiceState `json:"state" enum:"running,restarting,crashed,stopped,none"`
	RestartCount int          `json:"restart_count"`
	OOMKilled    bool         `json:"oom_killed"`
	// MemoryLimitMB is the active release's memory limit, set along with
	// OOMKilled so clients can say which limit was hit.
	MemoryLimitMB int        `json:"memory_limit_mb,omitempty"`
	ExitCode      int        `json:"exit_code"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
}

// ServiceSettings are a service's DB-held settings. Zero values mean "use
// the default".
type ServiceSettings struct {
	// Image is the image an image service runs (e.g. postgres:17) and the
	// default for image deploys. Image deploys set it to their image and
	// source deploys clear it, so empty means "built from source".
	Image                     string  `json:"image"`
	Port                      int     `json:"port" doc:"Container port. 0 means 8080 (published only once the service has a domain)"`
	HealthCheckPath           string  `json:"health_check_path" doc:"HTTP path; empty means a TCP check"`
	HealthCheckTimeoutSeconds int     `json:"health_check_timeout_seconds"`
	StartCommand              string  `json:"start_command"`
	DockerfilePath            string  `json:"dockerfile_path"`
	BuildCommand              string  `json:"build_command"`
	MemoryMB                  int     `json:"memory_mb"`
	CPUs                      float64 `json:"cpus"`
}

// Service is a container built from source or an image.
type Service struct {
	ID          int64  `json:"id"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Name        string `json:"name"`
	ServiceSettings
	// Stateful services have volumes and deploy with a recreate strategy.
	Stateful bool `json:"stateful"`
	// ActiveRelease is the number of the release serving traffic, if any.
	ActiveRelease *int64        `json:"active_release,omitempty"`
	Status        ServiceStatus `json:"status"`
	// PendingChanges counts staged changes to this service (settings,
	// volumes, its own variables). Environment-level ones are counted on
	// the environment.
	PendingChanges int        `json:"pending_changes"`
	CreatedAt      time.Time  `json:"created_at"`
	ArchivedAt     *time.Time `json:"archived_at,omitempty"`
}

// CreateServiceRequest creates a service. It doesn't deploy anything.
type CreateServiceRequest struct {
	Name                      string  `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"DNS-safe name, also the private hostname"`
	Image                     string  `json:"image,omitempty"`
	Port                      int     `json:"port,omitempty" minimum:"0" maximum:"65535"`
	HealthCheckPath           string  `json:"health_check_path,omitempty"`
	HealthCheckTimeoutSeconds int     `json:"health_check_timeout_seconds,omitempty" minimum:"0"`
	StartCommand              string  `json:"start_command,omitempty"`
	DockerfilePath            string  `json:"dockerfile_path,omitempty"`
	BuildCommand              string  `json:"build_command,omitempty"`
	MemoryMB                  int     `json:"memory_mb,omitempty" minimum:"0"`
	CPUs                      float64 `json:"cpus,omitempty" minimum:"0"`
}

// UpdateServiceRequest stages settings changes. Only fields that are set
// change. Staged changes go live on the next deploy of the service or when
// the environment's changes are applied.
type UpdateServiceRequest struct {
	Image                     *string  `json:"image,omitempty"`
	Port                      *int     `json:"port,omitempty" minimum:"0" maximum:"65535"`
	HealthCheckPath           *string  `json:"health_check_path,omitempty"`
	HealthCheckTimeoutSeconds *int     `json:"health_check_timeout_seconds,omitempty" minimum:"0"`
	StartCommand              *string  `json:"start_command,omitempty"`
	DockerfilePath            *string  `json:"dockerfile_path,omitempty"`
	BuildCommand              *string  `json:"build_command,omitempty"`
	MemoryMB                  *int     `json:"memory_mb,omitempty" minimum:"0"`
	CPUs                      *float64 `json:"cpus,omitempty" minimum:"0"`
	Apply                     bool     `json:"apply,omitempty" doc:"Also apply all staged changes in the environment"`
}

// ReleaseStatus is a step of the deploy state machine.
type ReleaseStatus string

// Release statuses: queued → building → starting → health_checking →
// active | failed. An active release becomes superseded when replaced.
const (
	ReleaseQueued         ReleaseStatus = "queued"
	ReleaseBuilding       ReleaseStatus = "building"
	ReleaseStarting       ReleaseStatus = "starting"
	ReleaseHealthChecking ReleaseStatus = "health_checking"
	ReleaseActive         ReleaseStatus = "active"
	ReleaseFailed         ReleaseStatus = "failed"
	ReleaseSuperseded     ReleaseStatus = "superseded"
)

// Done reports whether the release has finished deploying.
func (s ReleaseStatus) Done() bool {
	return s == ReleaseActive || s == ReleaseFailed || s == ReleaseSuperseded
}

// ReleaseSource says what created a release.
type ReleaseSource string

// Release sources.
const (
	SourceUpload   ReleaseSource = "upload"
	SourceImage    ReleaseSource = "image"
	SourceConfig   ReleaseSource = "config"
	SourceRollback ReleaseSource = "rollback"
	SourceRestart  ReleaseSource = "restart"
	SourcePromote  ReleaseSource = "promote"
)

// Release is an immutable snapshot of an image plus resolved config.
type Release struct {
	ID          int64         `json:"id"`
	Number      int64         `json:"number"`
	Project     string        `json:"project"`
	Environment string        `json:"environment"`
	Service     string        `json:"service"`
	Status      ReleaseStatus `json:"status" enum:"queued,building,starting,health_checking,active,failed,superseded"`
	Source      ReleaseSource `json:"source" enum:"upload,image,config,rollback,restart,promote"`
	// FromRelease is the release number a rollback or restart copied.
	FromRelease int64  `json:"from_release,omitempty"`
	ImageRef    string `json:"image_ref"`
	ImageID     string `json:"image_id"`
	// Error and ErrorCode explain a failed release.
	Error      string         `json:"error,omitempty"`
	ErrorCode  ErrorCode      `json:"error_code,omitempty"`
	Config     ReleaseConfig  `json:"config"`
	CreatedBy  string         `json:"created_by"`
	CreatedAt  time.Time      `json:"created_at"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Events     []ReleaseEvent `json:"events,omitempty" doc:"Progress lines; only included when fetching one release"`
}

// ReleaseConfig summarises a release's resolved config without secret values.
type ReleaseConfig struct {
	Port            int           `json:"port"`
	Published       bool          `json:"published" doc:"Port is published for routing"`
	StartCommand    string        `json:"start_command"`
	HealthCheckPath string        `json:"health_check_path"`
	MemoryMB        int           `json:"memory_mb"`
	CPUs            float64       `json:"cpus"`
	VariableKeys    []string      `json:"variable_keys"`
	Mounts          []VolumeMount `json:"mounts"`
	// FromRepoFile lists the settings dock.toml set (they win over the
	// service's settings and show as locked).
	FromRepoFile []string `json:"from_repo_file"`
}

// VolumeMount is a volume mounted into a release's container.
type VolumeMount struct {
	Volume string `json:"volume"`
	Path   string `json:"path"`
}

// ReleaseEvent is one progress line of a deploy.
type ReleaseEvent struct {
	// Seq increases with every release event (across all releases), so a
	// client can fetch history and then follow live events without
	// duplicates. Live events also carry it as the SSE `id:`.
	Seq           int64     `json:"seq"`
	ReleaseNumber int64     `json:"release"`
	Message       string    `json:"message"`
	CreatedAt     time.Time `json:"created_at"`
}

// DeployImageRequest deploys a prebuilt image.
type DeployImageRequest struct {
	Image string `json:"image,omitempty" doc:"Image reference; defaults to the service's image setting, which a deploy of another image updates"`
}

// BuildLogLine is one line of build output.
type BuildLogLine struct {
	ReleaseNumber int64  `json:"release"`
	Seq           int    `json:"seq" doc:"Zero-based line number within the build log"`
	Text          string `json:"text"`
}

// LogLine is one line of service output.
type LogLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream" enum:"stdout,stderr"`
	Text   string    `json:"text"`
}

// VariableScope says where a variable is defined.
type VariableScope string

// Variable scopes.
const (
	ScopeEnvironment VariableScope = "environment"
	ScopeService     VariableScope = "service"
)

// ChangeAction is what a staged change does.
type ChangeAction string

// Change actions.
const (
	ChangeAdd    ChangeAction = "add"
	ChangeUpdate ChangeAction = "update"
	ChangeRemove ChangeAction = "remove"
)

// Variable is one environment- or service-level variable. Value is the live
// value and is only included for admin callers asking with reveal=true.
// Variables that are only staged (Pending add) have no live value yet.
type Variable struct {
	Key       string        `json:"key"`
	Scope     VariableScope `json:"scope" enum:"environment,service"`
	Service   string        `json:"service,omitempty"`
	Value     *string       `json:"value,omitempty"`
	Pending   ChangeAction  `json:"pending,omitempty" enum:"add,update,remove" doc:"Staged change, if any"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// SetVariablesRequest stages setting and unsetting variables. Without
// Service it targets environment-level (shared) variables, which every
// service in the environment receives.
type SetVariablesRequest struct {
	Service string            `json:"service,omitempty"`
	Set     map[string]string `json:"set,omitempty"`
	Unset   []string          `json:"unset,omitempty"`
	Apply   bool              `json:"apply,omitempty" doc:"Also apply all staged changes in the environment"`
}

// PendingChanges are an environment's staged changes as a diff. Variable
// values are only included for admin callers asking with reveal=true.
type PendingChanges struct {
	Project     string           `json:"project"`
	Environment string           `json:"environment"`
	Count       int              `json:"count"`
	Variables   []VariableChange `json:"variables"`
	Settings    []SettingChange  `json:"settings"`
	Volumes     []VolumeAddition `json:"volumes"`
	// Affected names the services whose config applying would change:
	// those with changes of their own, and those whose resolved variables
	// change through shared variables or references to another service's.
	// The ones with an active release get a config release.
	Affected []string `json:"affected"`
	// LastFailure explains why changes are staged again: the release that
	// carried them failed, so they never went live.
	LastFailure string `json:"last_failure,omitempty"`
}

// CountFor counts the staged changes that belong to one service (its own
// variables, settings and volumes; not environment-level variables).
func (p PendingChanges) CountFor(service string) int {
	n := 0
	for _, v := range p.Variables {
		if v.Service == service {
			n++
		}
	}
	for _, s := range p.Settings {
		if s.Service == service {
			n++
		}
	}
	for _, v := range p.Volumes {
		if v.Service == service {
			n++
		}
	}
	return n
}

// VariableChange is one staged variable change.
type VariableChange struct {
	Key     string        `json:"key"`
	Scope   VariableScope `json:"scope" enum:"environment,service"`
	Service string        `json:"service,omitempty"`
	Action  ChangeAction  `json:"action" enum:"add,update,remove"`
	Old     *string       `json:"old,omitempty"`
	New     *string       `json:"new,omitempty"`
}

// SettingChange is one staged service setting change, with values rendered
// as text.
type SettingChange struct {
	Service string `json:"service"`
	Field   string `json:"field" example:"port"`
	Old     string `json:"old"`
	New     string `json:"new"`
}

// VolumeAddition is a staged new volume.
type VolumeAddition struct {
	Service   string `json:"service"`
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
}

// DiscardChangesRequest picks staged changes to discard. Without any
// selection (or without a body) every staged change in the environment is
// discarded. Selecting a change that isn't staged is an error.
type DiscardChangesRequest struct {
	Variables []VariableRef `json:"variables,omitempty"`
	Settings  []SettingRef  `json:"settings,omitempty"`
	Volumes   []VolumeRef   `json:"volumes,omitempty"`
}

// Empty reports whether nothing is selected, i.e. discard everything.
func (r DiscardChangesRequest) Empty() bool {
	return len(r.Variables)+len(r.Settings)+len(r.Volumes) == 0
}

// VariableRef names a variable: a service's, or a shared one when Service
// is empty.
type VariableRef struct {
	Key     string `json:"key"`
	Service string `json:"service,omitempty" doc:"Empty for a shared (environment-level) variable"`
}

// SettingRef names one setting of a service.
type SettingRef struct {
	Service string `json:"service"`
	Field   string `json:"field" example:"port"`
}

// VolumeRef names a volume of a service.
type VolumeRef struct {
	Service string `json:"service"`
	Name    string `json:"name"`
}

// AppliedChanges reports what applying staged changes started.
type AppliedChanges struct {
	// Releases are the config releases created, one per affected service
	// with an active release.
	Releases []Release `json:"releases"`
	// Skipped names services that weren't redeployed because a deploy was
	// already in progress; they pick up the change on their next release.
	Skipped []string `json:"skipped"`
}

// ChangeResult is returned by endpoints that stage a change: the
// environment's staged changes afterwards and, with apply, what was applied.
type ChangeResult struct {
	Pending PendingChanges  `json:"pending"`
	Applied *AppliedChanges `json:"applied,omitempty"`
}

// DomainStatus is a domain's DNS and certificate progress.
type DomainStatus string

// Domain statuses: added → waiting_for_dns → issuing_certificate → active,
// or error with a reason.
const (
	DomainAdded              DomainStatus = "added"
	DomainWaitingForDNS      DomainStatus = "waiting_for_dns"
	DomainIssuingCertificate DomainStatus = "issuing_certificate"
	DomainActive             DomainStatus = "active"
	DomainError              DomainStatus = "error"
)

// DNSRecord is a record the user must create for a domain to work.
type DNSRecord struct {
	Type  string `json:"type" enum:"A,CNAME"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Domain is a public hostname routed to a service.
type Domain struct {
	Hostname string `json:"hostname"`
	// URL is where the domain serves: https://, or http:// when the
	// server has TLS disabled.
	URL          string       `json:"url" example:"https://shop.example.com"`
	Project      string       `json:"project"`
	Environment  string       `json:"environment"`
	Service      string       `json:"service"`
	Generated    bool         `json:"generated"`
	RedirectTo   string       `json:"redirect_to,omitempty"`
	Status       DomainStatus `json:"status" enum:"added,waiting_for_dns,issuing_certificate,active,error"`
	StatusReason string       `json:"status_reason,omitempty"`
	// DNSRecords are the records to create; any one of them is enough.
	// Empty for generated domains (covered by the wildcard record).
	DNSRecords []DNSRecord `json:"dns_records"`
	// CheckedAt is when the daemon last checked the domain's DNS and
	// certificate, and ResolvedIPs what the hostname resolved to then.
	// Unset until the running daemon has checked it; active domains aren't
	// checked again.
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	ResolvedIPs []string   `json:"resolved_ips,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// AddDomainRequest adds a hostname to a service.
type AddDomainRequest struct {
	Hostname   string `json:"hostname" maxLength:"253"`
	RedirectTo string `json:"redirect_to,omitempty" doc:"Permanently redirect to this hostname instead of proxying"`
}

// DomainChange is the result of adding a domain. Release is set when the
// service had to be redeployed to publish its port.
type DomainChange struct {
	Domain  Domain   `json:"domain"`
	Release *Release `json:"release,omitempty"`
}

// Volume is a named Docker volume mounted into a service. Volumes are never
// deleted as a side effect.
type Volume struct {
	Name       string    `json:"name"`
	Service    string    `json:"service"`
	MountPath  string    `json:"mount_path"`
	DockerName string    `json:"docker_name"`
	Pending    bool      `json:"pending" doc:"Staged; mounted once applied"`
	CreatedAt  time.Time `json:"created_at"`
}

// AddVolumeRequest stages a volume, which makes the service stateful once
// applied.
type AddVolumeRequest struct {
	Name      string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$"`
	MountPath string `json:"mount_path" pattern:"^/" doc:"Absolute path inside the container"`
	Apply     bool   `json:"apply,omitempty" doc:"Also apply all staged changes in the environment"`
}

// Token is an API token (without its secret).
type Token struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Scope       Scope      `json:"scope" enum:"read,deploy,admin"`
	Project     string     `json:"project,omitempty"`
	Environment string     `json:"environment,omitempty"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

// CreateTokenRequest creates a token, optionally limited to one project or
// one environment of a project.
type CreateTokenRequest struct {
	Name          string `json:"name" minLength:"1" maxLength:"100"`
	Scope         Scope  `json:"scope" enum:"read,deploy,admin"`
	Project       string `json:"project,omitempty"`
	Environment   string `json:"environment,omitempty" doc:"Requires project"`
	ExpiresInDays int    `json:"expires_in_days,omitempty" minimum:"0" doc:"0 means never"`
}

// CreatedToken is returned once, on creation. Secret is never shown again.
type CreatedToken struct {
	Token  Token  `json:"token"`
	Secret string `json:"secret" example:"dock_3q2..."`
}

// User is a human who can log in to the UI.
type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      Scope     `json:"role" enum:"read,deploy,admin"`
	CreatedAt time.Time `json:"created_at"`
	// DisabledAt is set while the user can't log in.
	DisabledAt *time.Time `json:"disabled_at,omitempty"`
}

// ChangePasswordRequest changes the caller's own password. All their
// sessions end; the response carries a fresh one.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password" minLength:"12"`
}

// CreateUserRequest creates a user.
type CreateUserRequest struct {
	Email    string `json:"email" format:"email"`
	Name     string `json:"name" minLength:"1"`
	Password string `json:"password" minLength:"12"`
	Role     Scope  `json:"role" enum:"read,deploy,admin"`
}

// AuditEvent records one mutating action.
type AuditEvent struct {
	ID          int64     `json:"id"`
	Actor       string    `json:"actor"`
	ActorKind   string    `json:"actor_kind" enum:"user,token,system"`
	Action      string    `json:"action" example:"release.deploy"`
	Project     string    `json:"project,omitempty"`
	Environment string    `json:"environment,omitempty"`
	Target      string    `json:"target"`
	Summary     string    `json:"summary"`
	CreatedAt   time.Time `json:"created_at"`
}

// Server is a machine running a node runtime.
type Server struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Role         string     `json:"role" enum:"production,non-production"`
	Builder      bool       `json:"builder"`
	Local        bool       `json:"local" doc:"Runs the control plane"`
	Status       string     `json:"status" enum:"online,offline"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	Environments int        `json:"environments"`
}

// ProjectOverview is everything a project page needs in one call.
type ProjectOverview struct {
	Project      Project               `json:"project"`
	Environments []EnvironmentOverview `json:"environments"`
}

// EnvironmentOverview is one environment with its services.
type EnvironmentOverview struct {
	Environment Environment `json:"environment"`
	// PendingChanges counts all staged changes in the environment.
	PendingChanges int               `json:"pending_changes"`
	Services       []ServiceOverview `json:"services"`
}

// ServiceOverview is one service with its live state.
type ServiceOverview struct {
	Service       Service         `json:"service"`
	ActiveRelease *ReleaseSummary `json:"active_release,omitempty"`
	// Deploying is the in-flight release, if a deploy is running.
	Deploying *ReleaseSummary `json:"deploying,omitempty"`
	// LatestRelease is the newest release whatever its status, so a failed
	// last deploy shows without reading the release history.
	LatestRelease *ReleaseSummary `json:"latest_release,omitempty"`
	Domains       []Domain        `json:"domains"`
}

// ReleaseSummary is the short form of a release.
type ReleaseSummary struct {
	Number    int64         `json:"number"`
	Status    ReleaseStatus `json:"status" enum:"queued,building,starting,health_checking,active,failed,superseded"`
	Source    ReleaseSource `json:"source" enum:"upload,image,config,rollback,restart,promote"`
	ImageRef  string        `json:"image_ref"`
	ImageID   string        `json:"image_id"`
	CreatedBy string        `json:"created_by"`
	CreatedAt time.Time     `json:"created_at"`
	// Error and ErrorCode explain a failed release.
	Error     string    `json:"error,omitempty"`
	ErrorCode ErrorCode `json:"error_code,omitempty"`
}

// EventType names a live event. It is also the SSE `event:` field.
type EventType string

// Event types on the /events stream.
const (
	EventRelease       EventType = "release"
	EventReleaseEvent  EventType = "release_event"
	EventBuildLog      EventType = "build_log"
	EventServiceStatus EventType = "service_status"
	EventDomain        EventType = "domain"
	EventAudit         EventType = "audit"
	// EventDrift reports a difference between the DB and the runtime.
	EventDrift EventType = "drift"
	// EventLagged means the client was too slow and missed events; refetch.
	EventLagged EventType = "lagged"
)

// Event is a live update. Exactly one payload field is set, matching Type.
type Event struct {
	Type          EventType      `json:"type" enum:"release,release_event,build_log,service_status,domain,audit,drift,lagged"`
	Time          time.Time      `json:"time"`
	Project       string         `json:"project,omitempty"`
	Environment   string         `json:"environment,omitempty"`
	Service       string         `json:"service,omitempty"`
	Release       *Release       `json:"release,omitempty"`
	ReleaseEvent  *ReleaseEvent  `json:"release_event,omitempty"`
	BuildLog      *BuildLogLine  `json:"build_log,omitempty"`
	ServiceStatus *ServiceStatus `json:"service_status,omitempty"`
	Domain        *Domain        `json:"domain,omitempty"`
	Audit         *AuditEvent    `json:"audit,omitempty"`
	Message       string         `json:"message,omitempty"`
}
