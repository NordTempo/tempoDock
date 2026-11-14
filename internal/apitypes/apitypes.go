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

// GuidePage is one page of the user guide (docs/guide) as Markdown: the
// web UI's help shows it, matching the daemon's version.
type GuidePage struct {
	Page     string `json:"page" example:"deploying"`
	Markdown string `json:"markdown"`
}

// Health reports that the daemon is up.
type Health struct {
	Status  string `json:"status" example:"ok"`
	Version string `json:"version" example:"v0.1.0"`
}

// Me describes the caller: a logged-in user or an API token, acting in
// one org.
type Me struct {
	Kind string `json:"kind" enum:"user,token"`
	// Name is who the caller acts as in audit events: a user's email, a
	// token's name.
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	// DisplayName is a signed-in user's own name (PATCH /me changes it);
	// empty for tokens.
	DisplayName string `json:"display_name,omitempty"`
	// Org is the org the caller acts in; empty when a user with several
	// hasn't chosen one (the `org` parameter), and then Scope is too.
	Org   string `json:"org,omitempty"`
	Scope Scope  `json:"scope,omitempty" enum:"read,deploy,admin"`
	// Operator runs the platform: servers, backups, user accounts.
	Operator bool `json:"operator"`
	// Platform is the caller's platform role here (ADR 0080); empty for
	// none. PlatformLogin marks a platform credential (`dock login
	// --platform`, a platform token), which acts in no organization.
	Platform      PlatformRole `json:"platform,omitempty" enum:"operator,viewer"`
	PlatformLogin bool         `json:"platform_login,omitempty"`
	// Project and Environment are set when a token is limited to them.
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
	// TwoFactor is the second factor this session's sign-in used (passkey,
	// totp, recovery), or "login" for a `dock login` token approved from
	// such a session; empty for none.
	TwoFactor string `json:"two_factor,omitempty"`
	// TwoFactorRequired says what needs a second factor this session
	// didn't use (an org, or operating the platform), and what to do.
	TwoFactorRequired string `json:"two_factor_required,omitempty"`
	// SSO is the name of the identity provider this session signed in
	// through (ADR 0065) and SSOLabel its label, like "Google"; empty for
	// a password. Its step-ups sign in through it again.
	SSO      string `json:"sso,omitempty"`
	SSOLabel string `json:"sso_label,omitempty"`
	// AccessModel is how a person's groups and grants count here (ADR
	// 0080): enforce when they decide, shadow when their role still
	// does and the model only watches; empty for tokens and the platform.
	AccessModel AccessModelMode `json:"access_model,omitempty" enum:"shadow,enforce"`
}

// Org is an organization the caller belongs to (ADR 0027). It owns
// projects and tokens; the API's `org` parameter names it by slug.
type Org struct {
	Slug string `json:"slug" example:"acme"`
	Name string `json:"name"`
	// Role is the caller's role in the org.
	Role      Scope     `json:"role" enum:"read,deploy,admin"`
	CreatedAt time.Time `json:"created_at"`
	// RequireTwoFactor: members act in the org only after signing in
	// with a second factor (ADR 0057).
	RequireTwoFactor bool `json:"require_two_factor"`
}

// CreateOrgRequest creates an org with the caller as its admin.
type CreateOrgRequest struct {
	Slug string `json:"slug" minLength:"1" maxLength:"32" doc:"Lowercase letters, digits and dashes"`
	Name string `json:"name,omitempty" maxLength:"100" doc:"Defaults to the slug"`
}

// UpdateOrgRequest renames an org, changes its slug (its address), or
// both; an empty field stays as it is. The old slug keeps naming the org
// until another org takes it (ADR 0064).
type UpdateOrgRequest struct {
	Name string `json:"name,omitempty" maxLength:"100"`
	Slug string `json:"slug,omitempty" maxLength:"32" doc:"Lowercase letters, digits and dashes"`
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
	// Mail is how its latest mail went; nil when it was never emailed.
	Mail *InviteMail `json:"mail,omitempty"`
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
	// Mail says whether the link is being emailed, or why not (ADR 0079).
	Mail InviteMail `json:"mail"`
}

// InviteInfo is an open invite as the person invited sees it before
// accepting, signed in or not (ADR 0060): nothing more than the link's
// holder needs to decide.
type InviteInfo struct {
	Org     string `json:"org" doc:"The org's slug"`
	OrgName string `json:"org_name"`
	Email   string `json:"email"`
	Role    Scope  `json:"role" enum:"read,deploy,admin"`
	// InvitedBy is who created the invite, as the audit log names them.
	InvitedBy string    `json:"invited_by"`
	ExpiresAt time.Time `json:"expires_at"`
}

// InviteSignUpRequest creates an account for an invite's email and
// accepts the invite (ADR 0060).
type InviteSignUpRequest struct {
	Name     string `json:"name" maxLength:"200"`
	Password string `json:"password" maxLength:"1024"`
}

// UpdateMeRequest changes the caller's own account.
type UpdateMeRequest struct {
	// Name is the display name: 1 to 64 characters, trimmed.
	Name string `json:"name" maxLength:"200"`
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

// RenameProjectRequest changes a project's name and address, not its identity.
type RenameProjectRequest struct {
	Name string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"New DNS-safe name"`
	ID   int64  `json:"id,omitempty" minimum:"1" doc:"Expected project ID; prevents renaming a reused name"`
}

// DeleteProjectRequest confirms deleting an archived project for good
// (ADR 0078).
type DeleteProjectRequest struct {
	Confirm string `json:"confirm" doc:"The project's name, typed again to confirm"`
	ID      int64  `json:"id,omitempty" minimum:"1" doc:"Expected project ID; prevents deleting a project that took a reused name"`
}

// ProjectDeleted is what deleting an archived project removed, and what
// stays.
type ProjectDeleted struct {
	Project      string   `json:"project"`
	Environments int      `json:"environments"`
	Services     int      `json:"services"`
	Volumes      int      `json:"volumes" doc:"Docker volumes removed from the project's servers"`
	Notes        []string `json:"notes,omitempty" doc:"What stays: backups in restic repositories (with the tags that find them), and analytics events when the analytics store couldn't be reached"`
}

// ProjectRenamed lets open clients follow a project to its new address.
type ProjectRenamed struct {
	PreviousName string  `json:"previous_name"`
	Project      Project `json:"project"`
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
	ID      int64           `json:"id"`
	Project string          `json:"project"`
	Name    string          `json:"name"`
	Kind    EnvironmentKind `json:"kind" enum:"production,non_production"`
	// Server is the server the environment runs on, for platform
	// operators only: others never see server names (ADR 0062).
	Server string `json:"server,omitempty" doc:"Server the environment runs on; platform operators only"`
	// ServerStatus is that server's link status, for everyone: deploys
	// wait while it isn't online.
	ServerStatus ServerStatus `json:"server_status" enum:"online,offline,incompatible,joining,revoked" doc:"Whether the environment's server can be reached"`
	CreatedAt    time.Time    `json:"created_at"`
	// Lock is set while the environment is locked (ADR 0044), also while
	// it is opened for a while.
	Lock *EnvironmentLock `json:"lock,omitempty"`
	// Preview is set on a preview environment (ADR 0056): the pull
	// request it previews.
	Preview *PullRequestRef `json:"preview,omitempty" doc:"The pull request this preview environment belongs to"`
	// Moving is set while a move takes it to another server (ADR 0053),
	// on the project overview; MovingTo names that server, for platform
	// operators only.
	Moving   bool   `json:"moving,omitempty"`
	MovingTo string `json:"moving_to,omitempty"`
	// Sleep is set on an environment that may sleep on its own or is
	// asleep (ADR 0070).
	Sleep *EnvironmentSleep `json:"sleep,omitempty"`
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
	// ServiceStopped means the service was stopped on purpose (`dock
	// service stop`, ADR 0050): its active release has no container until
	// it is started or deployed again.
	ServiceStopped ServiceState = "stopped"
	// ServiceAsleep means the service's environment is asleep: its
	// container is stopped until the environment wakes (sleeping
	// environments).
	ServiceAsleep ServiceState = "asleep"
	// ServiceNone means no container serves the service: there is no
	// active release (never deployed, or archived), or its container is
	// gone or not running without having been stopped through Dock.
	ServiceNone ServiceState = "none"
)

// HealthState is the state of a service's ongoing health check (ADR 0043).
type HealthState string

// Health states.
const (
	HealthStarting  HealthState = "starting"
	HealthHealthy   HealthState = "healthy"
	HealthUnhealthy HealthState = "unhealthy"
)

// ServiceStatus is the observed runtime status of a service.
type ServiceStatus struct {
	State        ServiceState `json:"state" enum:"running,restarting,crashed,stopped,asleep,none"`
	RestartCount int          `json:"restart_count"`
	OOMKilled    bool         `json:"oom_killed"`
	// MemoryLimitMB is the active release's memory limit, set along with
	// OOMKilled so clients can say which limit was hit.
	MemoryLimitMB int `json:"memory_limit_mb,omitempty"`
	ExitCode      int `json:"exit_code"`
	// UpdatedAt is when the control plane last set the status, which a
	// daemon restart does too: not how long the service has been up.
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	// StartedAt is when the running container started, by Docker's
	// clock: how long the service has been up, across daemon restarts.
	// Empty unless it runs.
	StartedAt *time.Time `json:"started_at,omitempty" doc:"When the running container started; empty unless it runs"`
	// ServerOffline: the service's server can't be reached now, so State
	// is the last known one. Server and ServerStatus say which and why.
	// Only set in reads (services, overview, server pages), not in
	// service.status events.
	ServerOffline bool         `json:"server_offline,omitempty" doc:"The service's server can't be reached: state is the last known one"`
	Server        string       `json:"server,omitempty" doc:"With server_offline: the server's name, for platform operators only"`
	ServerStatus  ServerStatus `json:"server_status,omitempty" enum:"offline,incompatible,revoked" doc:"With server_offline: the server's status"`

	// Health is the ongoing health check's state while the container
	// runs (ADR 0043). Empty when the service has none: nothing to
	// probe, turned off, or a container from before checks existed.
	Health HealthState `json:"health,omitempty" enum:"starting,healthy,unhealthy" doc:"Ongoing health check state; empty without one"`
	// HealthRestarts counts the restarts the control plane made because
	// the health check kept failing, since the release went live (or the
	// daemon started). RestartCount doesn't include them.
	HealthRestarts int `json:"health_restarts,omitempty"`
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

	// The ongoing health check while the service runs (ADR 0043). It
	// probes what the deploy's check does: HealthCheckPath over HTTP, or
	// a TCP connect to the port.
	HealthCheckIntervalSeconds     int `json:"health_check_interval_seconds" doc:"Seconds between ongoing health probes while the service runs. 0 means 30; -1 turns the ongoing check off"`
	HealthCheckProbeTimeoutSeconds int `json:"health_check_probe_timeout_seconds" doc:"Seconds one ongoing probe may take. 0 means 5"`
	HealthCheckRetries             int `json:"health_check_retries" doc:"Failed probes in a row before the service counts as unhealthy and is restarted. 0 means 3"`
	HealthCheckStartPeriodSeconds  int `json:"health_check_start_period_seconds" doc:"Seconds after a (re)start in which failed probes don't count. 0 means the deploy's health check timeout"`
	// PreDeployCommand runs as a one-off job with each new release's image,
	// variables and environment network before the release gets traffic
	// (ADR 0045), e.g. database migrations. A non-zero exit fails the
	// release and the previous one keeps serving.
	PreDeployCommand        string `json:"pre_deploy_command" doc:"Runs with the new image before it gets traffic, e.g. migrations; empty runs nothing"`
	PreDeployTimeoutSeconds int    `json:"pre_deploy_timeout_seconds" doc:"How long the pre-deploy command may run. 0 means 600 (10 minutes)"`
	// PreDeploySnapshot snapshots every stateful service in the
	// environment before the pre-deploy command runs (ADR 0047); a failed
	// snapshot fails the release before the command runs.
	PreDeploySnapshot bool `json:"pre_deploy_snapshot" doc:"Snapshot the environment's stateful services before the pre-deploy command runs; a failed snapshot fails the release"`
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
	Database            DatabaseSetting `json:"database" enum:",postgres,mysql,redis,none"`
	DatabaseURLVariable string          `json:"database_url_variable"`
	// DataEngine is the detected database engine; the Data tab shows when
	// it is set.
	DataEngine DataEngine `json:"data_engine,omitempty" enum:"postgres,mysql,redis"`
	// Template is the database template the service was created from.
	Template Template `json:"template,omitempty" enum:"postgres,redis"`
	// ActiveRelease is the number of the release serving traffic, if any.
	ActiveRelease *int64        `json:"active_release,omitempty"`
	Status        ServiceStatus `json:"status"`
	// StoppedAt is set while the service is stopped on purpose (ADR 0050):
	// its active release has no container and its domains answer 503
	// until it is started or deployed again. StoppedBy says who stopped it.
	StoppedAt *time.Time `json:"stopped_at,omitempty" doc:"Set while the service is stopped (dock service stop): no container runs and its domains answer 503 until it is started or deployed"`
	StoppedBy string     `json:"stopped_by,omitempty"`
	EffectiveLimits
	// Usage is the latest resource sample of the active container (at most
	// a minute old), when there is one.
	Usage *ServiceUsage `json:"usage,omitempty"`
	// PendingChanges counts staged changes to this service (settings,
	// volumes, its own variables). Environment-level ones are counted on
	// the environment.
	PendingChanges int `json:"pending_changes"`
	// Staged is "create" for a service a project file staged that isn't
	// applied yet (it runs nothing), "archive" for one staged for archiving.
	Staged     string     `json:"staged,omitempty" enum:"create,archive"`
	CreatedAt  time.Time  `json:"created_at"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	// ArchiveNotes, only in an archive's answer, say what couldn't be
	// done at once: containers on a server that is offline (stopped when
	// it reconnects) or revoked (left running there).
	ArchiveNotes []string `json:"archive_notes,omitempty" doc:"Only in an archive's answer: containers on an offline server (stopped when it reconnects) or a revoked one (left running there)"`
}

// ServiceGitRequest is the repository a new service deploys from.
type ServiceGitRequest struct {
	Repo   string `json:"repo" minLength:"3" maxLength:"140" doc:"owner/name"`
	Branch string `json:"branch,omitempty" maxLength:"255" doc:"Branch every push to deploys; the repository's default branch when omitted"`
	Dir    string `json:"dir,omitempty" maxLength:"255" doc:"Subdirectory to build; empty for the repository root"`
}

// CreateServiceRequest creates a service. It doesn't deploy anything,
// unless Template is set: a template service (ADR 0019) gets its image,
// settings, volume and generated credentials from the template, ignores
// the other settings here, and deploys right away. With Git, the service
// is connected to the repository through the GitHub App and its
// branch's head deploys.
type CreateServiceRequest struct {
	// Git connects the new service through the org's GitHub App and
	// deploys the branch's head, as `dock git connect` then `dock git
	// deploy` would. A webhook connection is made with `dock git connect`
	// after creating the service.
	Git                       *ServiceGitRequest `json:"git,omitempty"`
	Name                      string             `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$" doc:"DNS-safe name, also the private hostname"`
	Template                  Template           `json:"template,omitempty" enum:"postgres,redis" doc:"Create a database from a template; other settings are ignored"`
	Image                     string             `json:"image,omitempty"`
	Port                      int                `json:"port,omitempty" minimum:"0" maximum:"65535"`
	HealthCheckPath           string             `json:"health_check_path,omitempty"`
	HealthCheckTimeoutSeconds int                `json:"health_check_timeout_seconds,omitempty" minimum:"0"`
	StartCommand              string             `json:"start_command,omitempty"`
	DockerfilePath            string             `json:"dockerfile_path,omitempty"`
	BuildCommand              string             `json:"build_command,omitempty"`
	MemoryMB                  int                `json:"memory_mb,omitempty" minimum:"0"`
	CPUs                      float64            `json:"cpus,omitempty" minimum:"0"`

	HealthCheckIntervalSeconds     int    `json:"health_check_interval_seconds,omitempty" minimum:"-1" maximum:"3600"`
	HealthCheckProbeTimeoutSeconds int    `json:"health_check_probe_timeout_seconds,omitempty" minimum:"0" maximum:"300"`
	HealthCheckRetries             int    `json:"health_check_retries,omitempty" minimum:"0" maximum:"100"`
	HealthCheckStartPeriodSeconds  int    `json:"health_check_start_period_seconds,omitempty" minimum:"0" maximum:"3600"`
	PreDeployCommand               string `json:"pre_deploy_command,omitempty"`
	PreDeployTimeoutSeconds        int    `json:"pre_deploy_timeout_seconds,omitempty" minimum:"0" maximum:"3600"`
	PreDeploySnapshot              bool   `json:"pre_deploy_snapshot,omitempty"`
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
	PreDeployCommand          *string  `json:"pre_deploy_command,omitempty" doc:"Empty string removes it"`
	PreDeployTimeoutSeconds   *int     `json:"pre_deploy_timeout_seconds,omitempty" minimum:"0" maximum:"3600"`
	PreDeploySnapshot         *bool    `json:"pre_deploy_snapshot,omitempty"`
	Apply                     bool     `json:"apply,omitempty" doc:"Also apply all staged changes in the environment"`

	HealthCheckIntervalSeconds     *int `json:"health_check_interval_seconds,omitempty" minimum:"-1" maximum:"3600"`
	HealthCheckProbeTimeoutSeconds *int `json:"health_check_probe_timeout_seconds,omitempty" minimum:"0" maximum:"300"`
	HealthCheckRetries             *int `json:"health_check_retries,omitempty" minimum:"0" maximum:"100"`
	HealthCheckStartPeriodSeconds  *int `json:"health_check_start_period_seconds,omitempty" minimum:"0" maximum:"3600"`
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
	Error      string        `json:"error,omitempty"`
	ErrorCode  ErrorCode     `json:"error_code,omitempty"`
	Config     ReleaseConfig `json:"config"`
	CreatedBy  string        `json:"created_by"`
	CreatedAt  time.Time     `json:"created_at"`
	FinishedAt *time.Time    `json:"finished_at,omitempty"`
	// LogsKept says the output of the release's removed container was
	// kept (ADR 0049), so its logs can be read; [logs] limits may have
	// dropped it since.
	LogsKept bool           `json:"logs_kept" doc:"The output of the release's removed container was kept, so its logs can be read until [logs] limits drop it"`
	Events   []ReleaseEvent `json:"events,omitempty" doc:"Progress lines; only included when fetching one release"`
	// NewErrors counts the service's error issues first seen in this
	// release (ADR 0062), for "N new errors since this release".
	NewErrors int `json:"new_errors" doc:"Error issues first seen in this release; only counted when fetching one release"`
	// Diagnosis names a failed release's diagnosis (ADR 0071) and its
	// likely cause.
	Diagnosis *DiagnosisRef `json:"diagnosis,omitempty" doc:"A failed release's diagnosis and its likely cause; read it whole with GET .../diagnoses/{id}"`
	// Scan is the release's image's scan (ADR 0074), on its own page.
	Scan *ImageScan `json:"scan,omitempty" doc:"The image's vulnerability scan, on GET of one release"`
	// PinnedRef is the repo digest a CI image was pulled by.
	PinnedRef string `json:"pinned_ref,omitempty"`
}

// ReleaseConfig summarises a release's resolved config without secret values.
type ReleaseConfig struct {
	Port            int    `json:"port"`
	Published       bool   `json:"published" doc:"Port is published for routing"`
	StartCommand    string `json:"start_command"`
	HealthCheckPath string `json:"health_check_path"`
	// PreDeployCommand is what ran (or would run) before the release got
	// traffic; empty if none.
	PreDeployCommand string `json:"pre_deploy_command,omitempty"`
	// PreDeploySnapshot says the environment's stateful services were (or
	// would be) snapshotted before the pre-deploy command.
	PreDeploySnapshot bool          `json:"pre_deploy_snapshot,omitempty"`
	MemoryMB          int           `json:"memory_mb"`
	CPUs              float64       `json:"cpus"`
	VariableKeys      []string      `json:"variable_keys"`
	Mounts            []VolumeMount `json:"mounts"`
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
	// Services are staged service creations and archives, and Domains
	// staged domain changes (from project files, ADR 0049).
	Services []ServiceStaging `json:"services"`
	Domains  []DomainStaging  `json:"domains"`
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
// variables, settings, volumes, domains and its creation or archive; not
// environment-level variables).
func (p PendingChanges) CountFor(service string) int {
	n := 0
	for _, s := range p.Services {
		if s.Service == service {
			n++
		}
	}
	for _, d := range p.Domains {
		if d.Service == service {
			n++
		}
	}
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

// ServiceStaging is a staged service creation (add) or archive (remove).
// A staged new service exists, can get variables and be deployed, but runs
// nothing until applied.
type ServiceStaging struct {
	Service string       `json:"service"`
	Action  ChangeAction `json:"action" enum:"add,remove"`
	// Summary describes a new service: its image, template or build.
	Summary string `json:"summary,omitempty"`
}

// DomainStaging is a staged domain addition or removal. A generated domain
// to add has no hostname yet.
type DomainStaging struct {
	Service    string       `json:"service"`
	Hostname   string       `json:"hostname,omitempty"`
	Action     ChangeAction `json:"action" enum:"add,remove"`
	Generated  bool         `json:"generated,omitempty"`
	RedirectTo string       `json:"redirect_to,omitempty"`
}

// DiscardChangesRequest picks staged changes to discard. Without any
// selection (or without a body) every staged change in the environment is
// discarded. Selecting a change that isn't staged is an error.
type DiscardChangesRequest struct {
	Variables []VariableRef `json:"variables,omitempty"`
	Settings  []SettingRef  `json:"settings,omitempty"`
	Volumes   []VolumeRef   `json:"volumes,omitempty"`
	// Services names services whose staged creation or archive to discard.
	// Discarding a creation deletes the service with everything staged on it.
	Services []string    `json:"services,omitempty"`
	Domains  []DomainRef `json:"domains,omitempty"`
}

// Empty reports whether nothing is selected, i.e. discard everything.
func (r DiscardChangesRequest) Empty() bool {
	return len(r.Variables)+len(r.Settings)+len(r.Volumes)+len(r.Services)+len(r.Domains) == 0
}

// DomainRef names a staged domain change of a service; an empty hostname
// is a generated domain still to be generated.
type DomainRef struct {
	Service  string `json:"service"`
	Hostname string `json:"hostname,omitempty"`
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
	// Failed are staged changes that couldn't be applied (a domain whose
	// hostname is taken, say) and stay staged, each with why.
	Failed []string `json:"failed,omitempty"`
}

// ChangeResult is returned by endpoints that stage a change: the
// environment's staged changes afterwards and, with apply, what was applied.
type ChangeResult struct {
	Pending PendingChanges  `json:"pending"`
	Applied *AppliedChanges `json:"applied,omitempty"`
}

// SwitchReferencesRequest points an environment's ${{from.KEY}}
// references at another service (ADR 0061): "use the restored database".
type SwitchReferencesRequest struct {
	// To is the service the references point at afterwards, live and in
	// the same environment.
	To string `json:"to" minLength:"1" example:"db-restored"`
	// DryRun only reports which services would change.
	DryRun bool `json:"dry_run,omitempty"`
}

// SwitchReferencesResult is what switching references changed, or would
// change with dry_run.
type SwitchReferencesResult struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Services are those whose config changes, by name.
	Services []string `json:"services"`
	// Releases are the config releases it started; empty for a dry run.
	Releases []ServiceRelease `json:"releases"`
}

// ServiceRelease names a release of a service.
type ServiceRelease struct {
	Service string `json:"service"`
	Number  int64  `json:"number"`
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
	Type  string `json:"type" enum:"A,AAAA,CNAME,TXT"`
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
	// Empty for generated domains under their server's base domain
	// (covered by its wildcard record).
	DNSRecords []DNSRecord `json:"dns_records"`
	// CheckedAt is when the daemon last checked the domain's DNS and
	// certificate, and ResolvedIPs what the hostname resolved to then.
	// Unset until the running daemon has checked it; active domains aren't
	// checked again.
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	ResolvedIPs []string   `json:"resolved_ips,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	// Verified says the service's org holds the hostname (ADR 0042): it
	// proved control with Verification, or needed no proof (generated,
	// under the server's base domain, from Cloudflare, or added by an
	// operator). Only verified domains are routed and get certificates.
	Verified bool `json:"verified"`
	// Verification is the TXT record that proves control, set while the
	// domain isn't verified. The daemon looks for it with every check.
	Verification *DNSRecord `json:"verification,omitempty"`
	// DNS is set for a domain Dock routes through its server's Cloudflare
	// tunnel (added from Cloudflare, ADR 0036): how far it is. Only in a
	// service's domain list and domain changes.
	DNS *DomainDNS `json:"dns,omitempty"`
}

// AddDomainRequest adds a hostname to a service.
type AddDomainRequest struct {
	Hostname   string `json:"hostname" maxLength:"253"`
	RedirectTo string `json:"redirect_to,omitempty" doc:"Permanently redirect to this hostname instead of proxying"`
	// Cloudflare adds a hostname in a zone a Cloudflare connection
	// manages and has Dock set up all it needs: a route through the
	// server's tunnel (created or adopted if Dock runs none) and its
	// proxied CNAME. On a direct server the tunnel serves such domains next
	// to the public IP (ADR 0036).
	Cloudflare bool `json:"cloudflare,omitempty"`
	// Analytics says what happens to a hostname that collected analytics
	// before, on another service (ADR 0037): keep (the default) carries
	// its site's history on; new starts a new site.
	Analytics string `json:"analytics,omitempty" enum:"keep,new" doc:"For a hostname with analytics history: keep its site (default) or start a new one"`
}

// DomainChange is the result of adding a domain. Release is set when the
// service had to be redeployed to publish its port.
type DomainChange struct {
	Domain  Domain   `json:"domain"`
	Release *Release `json:"release,omitempty"`
}

// AccessMode is who can open a service's domains (ADR 0030).
type AccessMode string

// Access modes: anyone, visitors with the password, or members signed in
// to Dock who can read the service.
const (
	AccessPublic   AccessMode = "public"
	AccessPassword AccessMode = "password"
	AccessLogin    AccessMode = "login"
)

// Access is who can open a service's domains. It covers every domain of
// the service, generated ones included; redirects only redirect.
type Access struct {
	Mode AccessMode `json:"mode" enum:"public,password,login"`
	// Username is what visitors type with the password (password mode).
	Username  string     `json:"username,omitempty"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// SetAccessRequest changes who can open a service's domains.
type SetAccessRequest struct {
	Mode     AccessMode `json:"mode" enum:"public,password,login"`
	Username string     `json:"username,omitempty" maxLength:"64" doc:"Password mode: the username visitors type"`
	Password string     `json:"password,omitempty" maxLength:"72" doc:"Password mode: at least 12 characters, kept only as a hash. Empty keeps the current password"`
}

// AccessGrantRequest asks to open a login-protected domain as the caller.
type AccessGrantRequest struct {
	Hostname string `json:"hostname" maxLength:"253"`
	// To is the path to open on it, "/" by default.
	To string `json:"to,omitempty" maxLength:"4096"`
}

// AccessGrant is where to send the browser to sign in to a protected
// domain: the domain's sign-in callback with a one-minute grant.
type AccessGrant struct {
	URL         string `json:"url"`
	Hostname    string `json:"hostname"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
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
	// Operator marks a legacy token that is both an org's and the
	// platform's (ADR 0080): it keeps working until it is split.
	Operator bool `json:"operator,omitempty"`
	// Platform marks a platform token: its role (operator, viewer), or
	// login for a `dock login --platform`. It acts in no organization.
	Platform string `json:"platform,omitempty" enum:"operator,viewer,login"`
	// Org is the organization of a legacy token, in the platform's list.
	Org string `json:"org,omitempty"`
	// User is the person a `dock login` acts for, in the platform's list.
	User string `json:"user,omitempty"`
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
	// Operator is refused since ADR 0080: an operator makes a platform
	// token in the console (CreatePlatformTokenRequest).
	Operator bool `json:"operator,omitempty" doc:"Refused: platform tokens are made in the console (dock platform token create)"`
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
	// TwoFactor is set when the user has a passkey or an authenticator app.
	TwoFactor bool `json:"two_factor"`
	// ExpiresAt is when an operator's membership from Join as operator
	// ends (ADR 0062); unset for every other member.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
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
	// Org is the org the account joins with Role; the caller's current
	// org when empty.
	Org  string `json:"org,omitempty" doc:"Slug of the organization the account joins; the caller's when empty"`
	Role Scope  `json:"role" enum:"read,deploy,admin"`
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
	// Drain is the server's running or paused drain (ADR 0063), for
	// operators.
	Drain   *DrainProgress `json:"drain,omitempty"`
	ID      int64          `json:"id"`
	Name    string         `json:"name"`
	Builder bool           `json:"builder" doc:"Source builds may run here"`
	Local   bool           `json:"local" doc:"Runs the control plane"`
	Status  ServerStatus   `json:"status" enum:"online,offline,incompatible,joining,revoked"`
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
	// Tunnel is the Cloudflare tunnel Dock manages for it (ADR 0036);
	// unset when it manages none.
	Tunnel *ServerTunnel `json:"tunnel,omitempty"`
	// TunnelPort is the port on 127.0.0.1 its cloudflared would forward
	// to; 0 when it can't take a tunnel now (a direct server without a
	// tunnel listener, or a remote one that is offline).
	TunnelPort int `json:"tunnel_port"`
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

// UpdateServerRequest changes a server's settings: its name, what may be
// placed on it, whether it builds, and its base domain. Only fields that
// are set change.
type UpdateServerRequest struct {
	// Name renames the server. Its environments, link and history stay;
	// only the name it is shown and addressed by changes.
	Name                 *string `json:"name,omitempty" minLength:"1" maxLength:"32"`
	AcceptsProduction    *bool   `json:"accepts_production,omitempty"`
	AcceptsNonProduction *bool   `json:"accepts_non_production,omitempty"`
	DisabledForPlacement *bool   `json:"disabled_for_placement,omitempty"`
	Builder              *bool   `json:"builder,omitempty"`
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
	// Tunnel has Dock create a Cloudflare tunnel for the server once it
	// joins, with the Cloudflare connection named Cloudflare (it may be
	// left out when there is one), and installs it in tunnel mode (ADR
	// 0036).
	Tunnel     bool   `json:"tunnel,omitempty"`
	Cloudflare string `json:"cloudflare,omitempty"`
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
	// SkipFailed: the service's release couldn't be created after earlier
	// ones started, which stops the promotion there; the services after
	// it are skipped with this reason too.
	SkipFailed SkipReason = "failed"
)

// SkippedService is a service a promotion left alone, and why.
type SkippedService struct {
	Service string     `json:"service"`
	Reason  SkipReason `json:"reason" enum:"missing_in_target,no_active_release,no_digest,deploy_in_progress,failed"`
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
	// MemoryParts say where memory_used_bytes goes; empty when the server
	// can't tell (no cgroup v2).
	MemoryParts []MemoryPart `json:"memory_parts,omitempty" doc:"Where the used memory goes, the biggest first"`
}

// MemoryGroup sorts what uses a server's memory.
type MemoryGroup string

// Memory groups.
const (
	MemoryApps     MemoryGroup = "apps"
	MemoryPlatform MemoryGroup = "platform"
	MemoryKernel   MemoryGroup = "kernel"
	MemorySystem   MemoryGroup = "system"
)

// MemoryPart is one user of a server's memory. For processes it is the
// memory they hold that can't be reclaimed (anonymous and shmem, no page
// cache), so an app's part is smaller than its usage, which counts
// active page cache like docker stats does.
type MemoryPart struct {
	Group MemoryGroup `json:"group" enum:"apps,platform,kernel,system"`
	// Name is an app's service, a part of Dock (BuildKit, Caddy, ...), a
	// systemd unit or a kind of kernel memory.
	Name string `json:"name"`
	// Environment is project/environment, for an app's service.
	Environment string `json:"environment,omitempty"`
	Bytes       int64  `json:"bytes"`
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

// TrafficRange is how far back traffic statistics go: 24h has a point per
// minute, 7d and 30d one per hour (ADR 0033).
type TrafficRange string

// Traffic ranges.
const (
	TrafficRange24h TrafficRange = "24h"
	TrafficRange7d  TrafficRange = "7d"
	TrafficRange30d TrafficRange = "30d"
)

// ServiceTraffic is what a service's domains served over a range: the
// requests Caddy proxied to it, counted from its access log (ADR 0033).
// Totals and Paths cover whole hours, from the start of the hour
// the range starts in (Since).
type ServiceTraffic struct {
	Project           string         `json:"project"`
	Environment       string         `json:"environment"`
	Service           string         `json:"service"`
	Range             TrafficRange   `json:"range" enum:"24h,7d,30d"`
	ResolutionSeconds int            `json:"resolution_seconds"`
	Since             time.Time      `json:"since" doc:"Start of Totals and Paths"`
	Totals            TrafficCounts  `json:"totals"`
	Points            []TrafficPoint `json:"points"`
	Paths             []TrafficPath  `json:"paths" doc:"The most requested methods and paths, most first; IDs in paths are :id"`
}

// TrafficCounts are the requests of an interval.
type TrafficCounts struct {
	Requests int64 `json:"requests"`
	// Status2xx counts 1xx responses too.
	Status2xx     int64   `json:"status_2xx"`
	Status3xx     int64   `json:"status_3xx"`
	Status4xx     int64   `json:"status_4xx"`
	Status5xx     int64   `json:"status_5xx"`
	Bytes         int64   `json:"bytes" doc:"Response bytes sent"`
	AvgDurationMS float64 `json:"avg_duration_ms"`
}

// TrafficPoint is a service's traffic in one interval.
type TrafficPoint struct {
	Time time.Time `json:"time" doc:"Start of the interval"`
	TrafficCounts
}

// TrafficPath is how often one method and path was requested.
type TrafficPath struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Requests int64  `json:"requests"`
	// Errors are the requests answered 5xx. P50MS, P95MS and P99MS are
	// how long requests took, from a histogram of every request (ADR
	// 0075), never sampled; 0 for paths counted before Dock kept it.
	Errors int64   `json:"errors"`
	P50MS  float64 `json:"p50_ms,omitempty"`
	P95MS  float64 `json:"p95_ms,omitempty"`
	P99MS  float64 `json:"p99_ms,omitempty"`
}

// TrafficUpdate is one minute tick's traffic of the services of one
// environment that had requests (the event names the project and
// environment). Point is the start of the minute they were stored as, so
// open 24h charts append them.
type TrafficUpdate struct {
	Point    time.Time             `json:"point"`
	Services []ServiceTrafficPoint `json:"services"`
}

// ServiceTrafficPoint is one service's minute in a TrafficUpdate.
type ServiceTrafficPoint struct {
	Service string       `json:"service"`
	Point   TrafficPoint `json:"point"`
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
	// LastFailedDeploy is the newest finished deploy (not a rollback or
	// restart) when it failed: it outlives a rollback to an earlier
	// release and clears once a later deploy of a new image or config
	// succeeds. It may be LatestRelease itself.
	LastFailedDeploy *ReleaseSummary `json:"last_failed_deploy,omitempty"`
	Domains          []Domain        `json:"domains"`
	// Backup is the latest backup run of a stateful service, if any.
	Backup *BackupRun `json:"backup,omitempty"`
	// Uptime sums up the service's uptime checks, if it has any.
	Uptime *ServiceUptime `json:"uptime,omitempty"`
	// Scan is the live image's scan (ADR 0074), once it has one.
	Scan *ImageScan `json:"scan,omitempty"`
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
	// Diagnosis is the release's newest diagnosis (ADR 0071), in the
	// project overview: why it failed, or why it crash loops.
	Diagnosis *DiagnosisRef `json:"diagnosis,omitempty" doc:"The release's newest diagnosis (why it failed, crash loops or is unhealthy), in the project overview"`
}

// EventType names a live event. It is also the SSE `event:` field.
type EventType string

// Event types on the /events stream.
const (
	EventRelease        EventType = "release"
	EventReleaseEvent   EventType = "release_event"
	EventBuildLog       EventType = "build_log"
	EventServiceStatus  EventType = "service_status"
	EventDomain         EventType = "domain"
	EventAudit          EventType = "audit"
	EventProjectRenamed EventType = "project_renamed"
	// EventDrift reports a difference between the DB and the runtime.
	EventDrift EventType = "drift"
	// EventLagged means the client was too slow and missed events; refetch.
	EventLagged EventType = "lagged"
	// EventUsage carries a minute's resource samples of a server or of an
	// environment's services.
	EventUsage EventType = "usage"
	// EventTraffic carries a minute's traffic of an environment's
	// services (ADR 0033).
	EventTraffic EventType = "traffic"
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
	// EventDisk carries a server's disk reading once a refresh of it
	// finished (ADR 0035). Operators only.
	EventDisk EventType = "disk"
	// EventAnalytics says the analytics store started, or failed to
	// start: read the status again (ADR 0037). No payload; operators
	// only.
	EventAnalytics EventType = "analytics"
	// EventEnvironmentLock: an environment was locked, unlocked or opened
	// for a while (ADR 0044). Lock is the new lock, nil when unlocked.
	EventEnvironmentLock EventType = "environment_lock"
	// EventAccessRequest: an access request was filed, decided or ran
	// (AccessRequest, without its arguments). Admins and the token that
	// asked get it.
	EventAccessRequest EventType = "access_request"
	// EventRun carries a service's command run as it starts and ends
	// (ADR 0049), without its output (read the run for that).
	EventRun EventType = "run"
	// EventUptime carries an uptime check whose state changed (ADR
	// 0055).
	EventUptime EventType = "uptime"
	// EventJob carries a service's scheduled job as it was set (ADR
	// 0048); with Message "removed", only its name.
	EventJob EventType = "job"
	// EventEnvironmentMove: an environment move went a step further, or
	// ended (ADR 0053). Move is the move.
	EventEnvironmentMove EventType = "environment_move"
	// EventServerDrain: a server drain went an environment further, or
	// ended (ADR 0063). Drain is the drain. Operators only.
	EventServerDrain EventType = "server_drain"
	// EventCloudflareRedirect carries a redirect whose state changed
	// (ADR 0068); with Message "removed", it is gone. The platform's
	// reach operators, an org's its admins.
	EventCloudflareRedirect EventType = "cloudflare_redirect"
	// EventSleep: an environment went to sleep, started waking, or woke.
	// Sleep is its new sleep state.
	EventSleep EventType = "sleep"
	// EventScan: an image's vulnerability scan changed (ADR 0074).
	EventScan EventType = "scan"
	// EventEmail: a mail was queued, sent, retried or given up on (ADR
	// 0079). An org's mail reaches its admins; every mail reaches
	// operators.
	EventEmail EventType = "email"
)

// Event is a live update. Exactly one payload field is set, matching Type.
type Event struct {
	Type           EventType           `json:"type" enum:"release,release_event,build_log,service_status,domain,audit,drift,lagged,usage,traffic,server,alert,update,project_renamed,disk,analytics,environment_lock,access_request,run,uptime,job,environment_move,server_drain,cloudflare_redirect,sleep,scan,email"`
	Time           time.Time           `json:"time"`
	ProjectRenamed *ProjectRenamed     `json:"project_renamed,omitempty"`
	Project        string              `json:"project,omitempty"`
	Environment    string              `json:"environment,omitempty"`
	Service        string              `json:"service,omitempty"`
	Release        *Release            `json:"release,omitempty"`
	ReleaseEvent   *ReleaseEvent       `json:"release_event,omitempty"`
	BuildLog       *BuildLogLine       `json:"build_log,omitempty"`
	ServiceStatus  *ServiceStatus      `json:"service_status,omitempty"`
	Domain         *Domain             `json:"domain,omitempty"`
	Audit          *AuditEvent         `json:"audit,omitempty"`
	Usage          *UsageUpdate        `json:"usage,omitempty"`
	Traffic        *TrafficUpdate      `json:"traffic,omitempty"`
	Server         *Server             `json:"server,omitempty"`
	Alert          *Alert              `json:"alert,omitempty"`
	Update         *UpdateStatus       `json:"update,omitempty"`
	Disk           *ServerDisk         `json:"disk,omitempty"`
	Lock           *EnvironmentLock    `json:"lock,omitempty"`
	AccessRequest  *AccessRequest      `json:"access_request,omitempty"`
	Run            *CommandRun         `json:"run,omitempty"`
	Uptime         *UptimeUpdate       `json:"uptime,omitempty"`
	Job            *Job                `json:"job,omitempty"`
	Move           *EnvironmentMove    `json:"move,omitempty"`
	Drain          *ServerDrain        `json:"drain,omitempty"`
	Redirect       *CloudflareRedirect `json:"redirect,omitempty"`
	Sleep          *EnvironmentSleep   `json:"sleep,omitempty"`
	Scan           *ImageScan          `json:"scan,omitempty"`
	Email          *EmailDelivery      `json:"email,omitempty"`
	Message        string              `json:"message,omitempty"`
}

// UsageUpdate is one minute tick's samples: a server's own (Host; only
// operators get these, as with server statistics) or the services of one
// environment (Services; the event names the project and environment).
// Server names the server of Host samples; service samples leave it
// empty, since only operators see server names. Point, when set, is
// the start of the 1-minute history point the samples were stored as, so
// open 1h and 24h charts append them instead of refetching.
type UsageUpdate struct {
	Server   string               `json:"server,omitempty"`
	Host     *ServerUsage         `json:"host,omitempty"`
	Services []ServiceUsageSample `json:"services,omitempty"`
	Point    *time.Time           `json:"point,omitempty"`
}

// ServiceUsageSample is one service's sample in a UsageUpdate.
type ServiceUsageSample struct {
	Service string       `json:"service"`
	Usage   ServiceUsage `json:"usage"`
}
