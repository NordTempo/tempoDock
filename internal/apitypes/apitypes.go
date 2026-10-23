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

// Me describes the caller: a logged-in user or an API token, acting in
// one org.
type Me struct {
	Kind  string `json:"kind" enum:"user,token"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	// Org is the org the caller acts in; empty when a user with several
	// hasn't chosen one (the `org` parameter), and then Scope is too.
	Org   string `json:"org,omitempty"`
	Scope Scope  `json:"scope,omitempty" enum:"read,deploy,admin"`
	// Operator runs the platform: servers, backups, user accounts.
	Operator bool `json:"operator"`
	// Project and Environment are set when a token is limited to them.
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// Org is an organization the caller belongs to (ADR 0027). It owns
// projects and tokens; the API's `org` parameter names it by slug.
type Org struct {
	Slug string `json:"slug" example:"acme"`
	Name string `json:"name"`
	// Role is the caller's role in the org.
	Role      Scope     `json:"role" enum:"read,deploy,admin"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateOrgRequest creates an org with the caller as its admin.
type CreateOrgRequest struct {
	Slug string `json:"slug" minLength:"1" maxLength:"32" doc:"Lowercase letters, digits and dashes"`
	Name string `json:"name,omitempty" maxLength:"100" doc:"Defaults to the slug"`
}

// UpdateOrgRequest renames an org. Its slug (its address) stays.
type UpdateOrgRequest struct {
	Name string `json:"name" minLength:"1" maxLength:"100"`
}

// SetMemberRoleRequest changes a member's role in the org.
type SetMemberRoleRequest struct {
	Role Scope `json:"role" enum:"read,deploy,admin"`
}

// Invite is an open invitation to the org for one email address.
type Invite struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Role      Scope     `json:"role" enum:"read,deploy,admin"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Expired is set once the link no longer works; revoke to list it no
	// more.
	Expired bool `json:"expired,omitempty"`
}

// CreateInviteRequest invites an email address with a role.
type CreateInviteRequest struct {
	Email string `json:"email" format:"email"`
	Role  Scope  `json:"role" enum:"read,deploy,admin"`
}

// CreatedInvite is returned once, on creation: the link's code is never
// shown again.
type CreatedInvite struct {
	Invite Invite `json:"invite"`
	// Code is the secret in the invite link, /invite/<code> on the web UI.
	Code string `json:"code"`
	// URL is the invite link on Dock's public address (ADR 0029); empty
	// without one, when clients build it from the address they use.
	URL string `json:"url,omitempty"`
}

// InviteInfo is an invite as the person invited sees it before accepting.
type InviteInfo struct {
	Org       string    `json:"org" doc:"The org's slug"`
	OrgName   string    `json:"org_name"`
	Email     string    `json:"email"`
	Role      Scope     `json:"role" enum:"read,deploy,admin"`
	ExpiresAt time.Time `json:"expires_at"`
	Status    string    `json:"status" enum:"open,accepted,revoked,expired"`
}

// OrgVariable is a variable every project of the org may reference as
// ${{org.KEY}}.
type OrgVariable struct {
	Key string `json:"key"`
	// Value is set only with reveal (admins).
	Value     *string   `json:"value,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
	// UsedBy is where a live or staged variable references it.
	UsedBy []VariableUse `json:"used_by"`
}

// VariableUse is a variable that references an org variable: a service's,
// or with no service an environment's shared one.
type VariableUse struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service,omitempty"`
}

// SetOrgVariableRequest sets an org variable's value.
type SetOrgVariableRequest struct {
	Value string `json:"value"`
}

// OrgVariableRollout is what redeploying an org variable's users started:
// a config release per running service, and those it couldn't redeploy.
type OrgVariableRollout struct {
	Started []RolloutService `json:"started"`
	Skipped []RolloutService `json:"skipped"`
}

// RolloutService is a service a rollout redeployed (Number, its new
// release) or left alone (Reason).
type RolloutService struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	Number      int64  `json:"number,omitempty"`
	Reason      string `json:"reason,omitempty"`
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
	// ArchiveNotes as on Service, plus networks left on such servers: only
	// in an archive's answer.
	ArchiveNotes []string `json:"archive_notes,omitempty" doc:"Only in an archive's answer: containers on an offline server (stopped when it reconnects) or a revoked one (left running there), and environment networks left on them"`
}

// CreateProjectRequest creates a project.
type CreateProjectRequest struct {
	Name string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"DNS-safe name"`
}

// EnvironmentKind decides which servers an environment may be placed on.
// It never changes after the environment is created.
type EnvironmentKind string

// Environment kinds.
const (
	KindProduction    EnvironmentKind = "production"
	KindNonProduction EnvironmentKind = "non_production"
)

// Environment is a set of services placed together on one server.
type Environment struct {
	ID        int64           `json:"id"`
	Project   string          `json:"project"`
	Name      string          `json:"name"`
	Kind      EnvironmentKind `json:"kind" enum:"production,non_production"`
	Server    string          `json:"server" doc:"Server the environment runs on"`
	CreatedAt time.Time       `json:"created_at"`
}

// CreateEnvironmentRequest creates an environment. It goes where the
// project's other environments of its kind are; the first of a kind picks
// the eligible server with the most free memory.
type CreateEnvironmentRequest struct {
	Name string          `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"DNS-safe name"`
	Kind EnvironmentKind `json:"kind,omitempty" enum:"production,non_production" doc:"Default: production for an environment named production, otherwise non_production. Never changes."`
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
	// ServerOffline: the service's server can't be reached now, so State
	// is the last known one. Server and ServerStatus say which and why.
	// Only set in reads (services, overview, server pages), not in
	// service.status events.
	ServerOffline bool         `json:"server_offline,omitempty" doc:"The service's server can't be reached: state is the last known one"`
	Server        string       `json:"server,omitempty" doc:"With server_offline: the server's name"`
	ServerStatus  ServerStatus `json:"server_status,omitempty" enum:"offline,incompatible,revoked" doc:"With server_offline: the server's status"`
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
	// Database overrides database detection (ADR 0016); empty detects from
	// the image. DatabaseURLVariable names the variable the panel reads
	// connection details from, e.g. DATABASE_URL or api.DATABASE_URL.
	// Both apply immediately and are never staged.
	Database            DatabaseSetting `json:"database" enum:",postgres,none"`
	DatabaseURLVariable string          `json:"database_url_variable"`
	// DataEngine is the detected database engine; the Data tab shows when
	// it is set.
	DataEngine DataEngine `json:"data_engine,omitempty" enum:"postgres"`
	// Template is the database template the service was created from.
	Template Template `json:"template,omitempty" enum:"postgres,redis"`
	// ActiveRelease is the number of the release serving traffic, if any.
	ActiveRelease *int64        `json:"active_release,omitempty"`
	Status        ServiceStatus `json:"status"`
	EffectiveLimits
	// Usage is the latest resource sample of the active container (at most
	// a minute old), when there is one.
	Usage *ServiceUsage `json:"usage,omitempty"`
	// PendingChanges counts staged changes to this service (settings,
	// volumes, its own variables). Environment-level ones are counted on
	// the environment.
	PendingChanges int        `json:"pending_changes"`
	CreatedAt      time.Time  `json:"created_at"`
	ArchivedAt     *time.Time `json:"archived_at,omitempty"`
	// ArchiveNotes, only in an archive's answer, say what couldn't be
	// done at once: containers on a server that is offline (stopped when
	// it reconnects) or revoked (left running there).
	ArchiveNotes []string `json:"archive_notes,omitempty" doc:"Only in an archive's answer: containers on an offline server (stopped when it reconnects) or a revoked one (left running there)"`
}

// CreateServiceRequest creates a service. It doesn't deploy anything,
// unless Template is set: a template service (ADR 0019) gets its image,
// settings, volume and generated credentials from the template, ignores
// the other settings here, and deploys right away.
type CreateServiceRequest struct {
	Name                      string   `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"DNS-safe name, also the private hostname"`
	Template                  Template `json:"template,omitempty" enum:"postgres,redis" doc:"Create a database from a template; other settings are ignored"`
	Image                     string   `json:"image,omitempty"`
	Port                      int      `json:"port,omitempty" minimum:"0" maximum:"65535"`
	HealthCheckPath           string   `json:"health_check_path,omitempty"`
	HealthCheckTimeoutSeconds int      `json:"health_check_timeout_seconds,omitempty" minimum:"0"`
	StartCommand              string   `json:"start_command,omitempty"`
	DockerfilePath            string   `json:"dockerfile_path,omitempty"`
	BuildCommand              string   `json:"build_command,omitempty"`
	MemoryMB                  int      `json:"memory_mb,omitempty" minimum:"0"`
	CPUs                      float64  `json:"cpus,omitempty" minimum:"0"`
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
	// ImageDigest is the image's manifest digest in the platform registry
	// (sha256:...), which other servers pull and promotions carry; empty
	// for images that were never pushed there.
	ImageDigest string `json:"image_digest,omitempty"`
	// Commit is the commit the image was built from (push-to-deploy, ADR
	// 0018); releases that reuse an image keep it.
	Commit *Commit `json:"commit,omitempty"`
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
	// Origin is "cli_login" for tokens created by approving `dock login`
	// in the browser, else "manual".
	Origin string `json:"origin" enum:"manual,cli_login"`
	// Operator tokens may run the platform, like operator users.
	Operator bool `json:"operator,omitempty"`
	// Data is the token's own database access; without it the token
	// follows the data policy's tokens rule (ADR 0016).
	Data *DataTokenAccess `json:"data,omitempty"`
}

// CreateTokenRequest creates a token, optionally limited to one project or
// one environment of a project.
type CreateTokenRequest struct {
	Name          string `json:"name" minLength:"1" maxLength:"100"`
	Scope         Scope  `json:"scope" enum:"read,deploy,admin"`
	Project       string `json:"project,omitempty"`
	Environment   string `json:"environment,omitempty" doc:"Requires project"`
	ExpiresInDays int    `json:"expires_in_days,omitempty" minimum:"0" doc:"0 means never"`
	// Operator makes a platform operator token (operators only): admin
	// scope, no project or environment limit.
	Operator bool `json:"operator,omitempty"`
	// Data overrides the data policy's tokens rule for this token.
	Data *DataTokenAccess `json:"data,omitempty"`
}

// CreatedToken is returned once, on creation. Secret is never shown again.
type CreatedToken struct {
	Token  Token  `json:"token"`
	Secret string `json:"secret" example:"dock_3q2..."`
}

// User is a human who can log in to the UI, as a member of an org.
type User struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	// Role is the user's role in the org.
	Role Scope `json:"role" enum:"read,deploy,admin"`
	// Operator runs the platform: servers, backups, user accounts.
	Operator  bool      `json:"operator"`
	CreatedAt time.Time `json:"created_at"`
	// DisabledAt is set while the user can't log in.
	DisabledAt *time.Time `json:"disabled_at,omitempty"`
	// Sessions counts the user's live UI sessions.
	Sessions int `json:"sessions"`
	// Tokens counts the usable (not revoked or expired) tokens they created.
	Tokens int `json:"tokens"`
}

// ResetPasswordRequest sets another user's password. Without a password
// one is generated and returned once. Their sessions end either way.
type ResetPasswordRequest struct {
	Password string `json:"password,omitempty" minLength:"12" doc:"New password; omit to generate one"`
}

// ResetPasswordResult is a user after a password reset.
type ResetPasswordResult struct {
	User User `json:"user"`
	// Password is the generated password, shown once. Empty when the
	// request set one.
	Password string `json:"password,omitempty"`
}

// ChangePasswordRequest changes the caller's own password. All their
// sessions end; the response carries a fresh one.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password" minLength:"12"`
}

// CreateUserRequest creates a user account (operators only) and adds it
// to the caller's org with Role.
type CreateUserRequest struct {
	Email    string `json:"email" format:"email"`
	Name     string `json:"name" minLength:"1"`
	Password string `json:"password" minLength:"12"`
	Role     Scope  `json:"role" enum:"read,deploy,admin"`
	// Operator makes the user a platform operator too.
	Operator bool `json:"operator,omitempty"`
}

// AuditEvent records one mutating action.
type AuditEvent struct {
	ID          int64  `json:"id"`
	Actor       string `json:"actor"`
	ActorKind   string `json:"actor_kind" enum:"user,token,system"`
	Action      string `json:"action" example:"release.deploy"`
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
	Target      string `json:"target"`
	Summary     string `json:"summary"`
	// DetailID points at an AuditDetail (SQL, rows, duration, outcome) for
	// data.* events.
	DetailID  *int64    `json:"detail_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ServerStatus is a server's link status (ADR 0014 section 5).
type ServerStatus string

// Server statuses. The local server is always online.
const (
	// ServerOnline: connected and compatible; deploys go through.
	ServerOnline ServerStatus = "online"
	// ServerOffline: not connected since LastSeenAt; deploys are rejected
	// with server_offline.
	ServerOffline ServerStatus = "offline"
	// ServerIncompatible: connected, but its dock is too old for this
	// control plane; nothing is deployed there until it is upgraded.
	ServerIncompatible ServerStatus = "incompatible"
	// ServerJoining: a join token was issued (`dock server add`) and the
	// server hasn't joined with it yet.
	ServerJoining ServerStatus = "joining"
	// ServerRevoked: its key was revoked; it stays out until it joins
	// again with a new token. Its environments stay.
	ServerRevoked ServerStatus = "revoked"
)

// Ingress is how a server receives public traffic (ADR 0013).
type Ingress string

// Ingress modes.
const (
	// IngressDirect: a public IP; Caddy issues certificates.
	IngressDirect Ingress = "direct"
	// IngressCloudflareTunnel: cloudflared forwards from Cloudflare's
	// edge, which terminates TLS. Custom domains are CNAMEs to
	// TunnelHostname.
	IngressCloudflareTunnel Ingress = "cloudflare_tunnel"
)

// Server is a machine running a node runtime. A server that is still
// joining (a pending join token) has ID 0 and no facts yet.
type Server struct {
	ID      int64        `json:"id"`
	Name    string       `json:"name"`
	Builder bool         `json:"builder" doc:"Source builds may run here"`
	Local   bool         `json:"local" doc:"Runs the control plane"`
	Status  ServerStatus `json:"status" enum:"online,offline,incompatible,joining,revoked"`
	// Version is the dock version the server runs (reported when it
	// connects).
	Version    string     `json:"version"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	// ConnectedAt is when its current (or last) link came up, and
	// DisconnectReason why the last one ended.
	ConnectedAt      *time.Time `json:"connected_at,omitempty"`
	DisconnectReason string     `json:"disconnect_reason,omitempty"`
	// RevokedAt is set while the server is revoked.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	// JoinExpiresAt is when the join token of a joining server expires.
	JoinExpiresAt *time.Time `json:"join_expires_at,omitempty"`
	// KeyFingerprint is the server's pinned link key (public, not a
	// secret); empty for the local server and revoked ones.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	// Ingress, PublicIPs, TunnelHostname and DockerVersion are facts the
	// server reports when it connects (read-only).
	Ingress        Ingress  `json:"ingress" enum:"direct,cloudflare_tunnel"`
	PublicIPs      []string `json:"public_ips" doc:"What DNS records point at (direct ingress)"`
	TunnelHostname string   `json:"tunnel_hostname,omitempty" doc:"CNAME target of its Cloudflare tunnel"`
	DockerVersion  string   `json:"docker_version,omitempty"`
	// BaseDomain is the base of generated hostnames for environments on
	// this server: <service>-<project>[-<env>].<base_domain>.
	BaseDomain string `json:"base_domain"`
	// AcceptsProduction and AcceptsNonProduction say which environment
	// kinds may be placed here.
	AcceptsProduction    bool `json:"accepts_production"`
	AcceptsNonProduction bool `json:"accepts_non_production"`
	// DisabledForPlacement stops new automatic placements; what is
	// already here stays, and admins can still pin projects here.
	DisabledForPlacement bool `json:"disabled_for_placement"`
	// MemoryBytes is the server's total memory (0 until it reported it).
	MemoryBytes int64 `json:"memory_bytes"`
	// ReservedMemoryBytes is the sum of the memory limits of the services
	// placed here (the default limit for those without one).
	ReservedMemoryBytes int64 `json:"reserved_memory_bytes"`
	Projects            int   `json:"projects" doc:"Projects with environments placed here"`
	Environments        int   `json:"environments"`
	// Usage is the latest minute sample, kept in memory. Only unlimited
	// admins get it, as with server statistics.
	Usage *ServerUsage `json:"usage,omitempty"`
}

// EffectiveLimits are the limits a service's releases get.
type EffectiveLimits struct {
	// EffectiveMemoryMB and EffectiveCPUs are memory_mb and cpus, or the
	// server's defaults when those are 0.
	EffectiveMemoryMB int     `json:"effective_memory_mb"`
	EffectiveCPUs     float64 `json:"effective_cpus"`
	// MemoryFromDefault and CPUsFromDefault say the effective limit is the
	// default rather than set on the service.
	MemoryFromDefault bool `json:"memory_from_default"`
	CPUsFromDefault   bool `json:"cpus_from_default"`
}

// UpdateServerRequest changes a server's settings: what may be placed on
// it, whether it builds, and its base domain. Only fields that are set
// change.
type UpdateServerRequest struct {
	AcceptsProduction    *bool `json:"accepts_production,omitempty"`
	AcceptsNonProduction *bool `json:"accepts_non_production,omitempty"`
	DisabledForPlacement *bool `json:"disabled_for_placement,omitempty"`
	Builder              *bool `json:"builder,omitempty"`
	// BaseDomain is a domain like up.example.dev with a wildcard DNS
	// record pointing at the server (or its tunnel); "" removes it.
	BaseDomain *string `json:"base_domain,omitempty" maxLength:"253"`
}

// AddServerRequest starts adding a server (`dock server add`): it creates
// a one-time join token for it.
type AddServerRequest struct {
	Name                 string `json:"name" minLength:"1" maxLength:"32"`
	AcceptsProduction    bool   `json:"accepts_production"`
	AcceptsNonProduction bool   `json:"accepts_non_production"`
	Builder              bool   `json:"builder"`
}

// AddedServer is a new join token. Token is shown only this once; run
// Command on the new server before ExpiresAt.
type AddedServer struct {
	Server    Server    `json:"server"`
	Token     string    `json:"token"`
	Command   string    `json:"command" doc:"Install command to run on the new server"`
	ExpiresAt time.Time `json:"expires_at"`
}

// PromoteRequest promotes the images running in one environment to
// another of the same project (`dock promote staging production`).
type PromoteRequest struct {
	From string `json:"from" minLength:"1"`
	To   string `json:"to" minLength:"1"`
	// Services limits the promotion to these services; empty means every
	// service with an active release in From.
	Services []string `json:"services,omitempty"`
}

// PromoteResult lists what a promotion started and what it skipped.
// Promoted releases deploy one at a time in the listed order; when one
// fails, the ones after it fail without starting.
type PromoteResult struct {
	From     string            `json:"from"`
	To       string            `json:"to"`
	Promoted []PromotedService `json:"promoted"`
	Skipped  []SkippedService  `json:"skipped"`
}

// PromotedService is one service's new release in the target environment.
type PromotedService struct {
	Service string `json:"service"`
	// FromRelease is the number of the source environment's release that
	// was promoted.
	FromRelease int64   `json:"from_release"`
	Release     Release `json:"release"`
}

// SkipReason says why a promotion skipped a service.
type SkipReason string

// Reasons a promotion skips a service.
const (
	// SkipMissingInTarget: the target environment has no service of that
	// name. Promote never creates services.
	SkipMissingInTarget SkipReason = "missing_in_target"
	// SkipNoActiveRelease: nothing is running in the source environment.
	SkipNoActiveRelease SkipReason = "no_active_release"
	// SkipNoDigest: the image isn't in the registry and can't reach the
	// target's server; redeploy it in the source environment first.
	SkipNoDigest SkipReason = "no_digest"
	// SkipDeployInProgress: the target service is deploying already.
	SkipDeployInProgress SkipReason = "deploy_in_progress"
)

// SkippedService is a service a promotion left alone, and why.
type SkippedService struct {
	Service string     `json:"service"`
	Reason  SkipReason `json:"reason" enum:"missing_in_target,no_active_release,no_digest,deploy_in_progress"`
	Message string     `json:"message"`
}

// ServerDetail is one server (with its latest resource sample) and what
// is placed on it.
type ServerDetail struct {
	Server     Server            `json:"server"`
	Placements []ServerPlacement `json:"placements"`
	// Services are the services of the environments placed here, the
	// biggest memory users first.
	Services []ServerService `json:"services"`
}

// ServerService is a service on a server with its latest usage.
type ServerService struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Name        string `json:"name"`
	// ActiveRelease is the number of the release serving traffic, if any.
	ActiveRelease *int64        `json:"active_release,omitempty"`
	Status        ServiceStatus `json:"status"`
	EffectiveLimits
	// Usage is the latest sample of the active container, when there is
	// one.
	Usage *ServiceUsage `json:"usage,omitempty"`
}

// ServerPlacement is one project's environments of one kind on a server.
type ServerPlacement struct {
	Project      string          `json:"project"`
	Kind         EnvironmentKind `json:"kind" enum:"production,non_production"`
	Pinned       bool            `json:"pinned" doc:"Set by an admin rather than chosen automatically"`
	Environments []string        `json:"environments"`
}

// Placement is where a project's environments of one kind live.
type Placement struct {
	Kind   EnvironmentKind `json:"kind" enum:"production,non_production"`
	Server string          `json:"server"`
	Pinned bool            `json:"pinned"`
}

// PinPlacementRequest pins a project's environments of a kind to a server,
// before any of them exist.
type PinPlacementRequest struct {
	Server string `json:"server" minLength:"1"`
}

// ServerUsage is one resource sample of a server.
type ServerUsage struct {
	Time time.Time `json:"time"`
	// CPUPercent is the use of all cores together: 100 means every core
	// is busy.
	CPUPercent       float64 `json:"cpu_percent"`
	CPUs             int     `json:"cpus"`
	MemoryUsedBytes  int64   `json:"memory_used_bytes" doc:"Total minus available, as free(1) shows"`
	MemoryTotalBytes int64   `json:"memory_total_bytes"`
	Load1            float64 `json:"load1"`
	// DiskUsedBytes and DiskTotalBytes are the filesystem holding Docker's
	// data root (images, volumes, container logs): the one that fills up.
	// History's disk_used_bytes is the same number.
	DiskUsedBytes  int64 `json:"disk_used_bytes"`
	DiskTotalBytes int64 `json:"disk_total_bytes"`
	// Disks are the filesystems dock uses: Docker's data root first, then
	// dock's data dir when it is on another filesystem.
	Disks []DiskUsage `json:"disks"`
}

// DiskUsage is one filesystem dock uses.
type DiskUsage struct {
	Paths      []string `json:"paths"`
	UsedBytes  int64    `json:"used_bytes"`
	TotalBytes int64    `json:"total_bytes"`
}

// ServiceUsage is one resource sample of a service's active container.
type ServiceUsage struct {
	Time time.Time `json:"time"`
	// CPUPercent is relative to one core, like docker stats: a service
	// using two full cores shows 200.
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryBytes int64   `json:"memory_bytes" doc:"Working set: usage minus inactive page cache, as docker stats shows"`
	// MemoryLimitBytes is the container's hard limit; 0 means none.
	MemoryLimitBytes   int64 `json:"memory_limit_bytes"`
	IOReadBytesPerSec  int64 `json:"io_read_bytes_per_sec"`
	IOWriteBytesPerSec int64 `json:"io_write_bytes_per_sec"`
}

// StatsRange is how far back statistics go: 1h and 24h have a point per
// minute, 7d a point per 10 minutes.
type StatsRange string

// Statistics ranges.
const (
	Range1h  StatsRange = "1h"
	Range24h StatsRange = "24h"
	Range7d  StatsRange = "7d"
)

// ServerStats is a server's statistics history.
type ServerStats struct {
	Server            string             `json:"server"`
	Range             StatsRange         `json:"range" enum:"1h,24h,7d"`
	ResolutionSeconds int                `json:"resolution_seconds"`
	Current           *ServerUsage       `json:"current,omitempty"`
	Points            []ServerStatsPoint `json:"points"`
}

// ServerStatsPoint is a server's average over one interval.
type ServerStatsPoint struct {
	Time            time.Time `json:"time" doc:"Start of the interval"`
	CPUPercent      float64   `json:"cpu_percent"`
	MemoryUsedBytes int64     `json:"memory_used_bytes"`
	// DiskUsedBytes is the use of the filesystem holding Docker's data
	// root, as in ServerUsage.
	DiskUsedBytes int64   `json:"disk_used_bytes"`
	Load1         float64 `json:"load1"`
}

// ServiceStats is a service's statistics history. It spans releases.
type ServiceStats struct {
	Project           string              `json:"project"`
	Environment       string              `json:"environment"`
	Service           string              `json:"service"`
	Range             StatsRange          `json:"range" enum:"1h,24h,7d"`
	ResolutionSeconds int                 `json:"resolution_seconds"`
	Current           *ServiceUsage       `json:"current,omitempty"`
	Points            []ServiceStatsPoint `json:"points"`
}

// ServiceStatsPoint is a service's average over one interval.
type ServiceStatsPoint struct {
	Time               time.Time `json:"time" doc:"Start of the interval"`
	CPUPercent         float64   `json:"cpu_percent"`
	MemoryBytes        int64     `json:"memory_bytes"`
	IOReadBytesPerSec  int64     `json:"io_read_bytes_per_sec"`
	IOWriteBytesPerSec int64     `json:"io_write_bytes_per_sec"`
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
	// Backup is the latest backup run of a stateful service, if any.
	Backup *BackupRun `json:"backup,omitempty"`
}

// ReleaseSummary is the short form of a release.
type ReleaseSummary struct {
	Number    int64         `json:"number"`
	Status    ReleaseStatus `json:"status" enum:"queued,building,starting,health_checking,active,failed,superseded"`
	Source    ReleaseSource `json:"source" enum:"upload,image,config,rollback,restart,promote"`
	ImageRef  string        `json:"image_ref"`
	ImageID   string        `json:"image_id"`
	Commit    *Commit       `json:"commit,omitempty"`
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
	// EventUsage carries a minute's resource samples of a server or of an
	// environment's services.
	EventUsage EventType = "usage"
	// EventServer carries a server whose status or facts changed: it
	// connected, disconnected, joined, was revoked, changed or removed
	// (Message "removed").
	EventServer EventType = "server"
	// EventAlert carries an alert that opened, changed or resolved (ADR
	// 0020). Only unlimited admins get it.
	EventAlert EventType = "alert"
	// EventUpdate carries the update status whenever an update moves on
	// (ADR 0028), without the releases. Operators only.
	EventUpdate EventType = "update"
)

// Event is a live update. Exactly one payload field is set, matching Type.
type Event struct {
	Type          EventType      `json:"type" enum:"release,release_event,build_log,service_status,domain,audit,drift,lagged,usage,server,alert,update"`
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
	Usage         *UsageUpdate   `json:"usage,omitempty"`
	Server        *Server        `json:"server,omitempty"`
	Alert         *Alert         `json:"alert,omitempty"`
	Update        *UpdateStatus  `json:"update,omitempty"`
	Message       string         `json:"message,omitempty"`
}

// UsageUpdate is one minute tick's samples: a server's own (Host; only
// unlimited admins get these, as with server statistics) or the services
// of one environment (Services; the event names the project and
// environment). Server names the server either way. Point, when set, is
// the start of the 1-minute history point the samples were stored as, so
// open 1h and 24h charts append them instead of refetching.
type UsageUpdate struct {
	Server   string               `json:"server"`
	Host     *ServerUsage         `json:"host,omitempty"`
	Services []ServiceUsageSample `json:"services,omitempty"`
	Point    *time.Time           `json:"point,omitempty"`
}

// ServiceUsageSample is one service's sample in a UsageUpdate.
type ServiceUsageSample struct {
	Service string       `json:"service"`
	Usage   ServiceUsage `json:"usage"`
}
