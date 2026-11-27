package apitypes

import "time"

// Alerts and notification channels (ADR 0020). An alert about a project
// or service is its org's; one about a server or Dock itself is the
// platform's (ADR 0062). Channels belong to an org or to the platform and
// receive only its alerts.

// AlertSeverity is how urgent an alert is.
type AlertSeverity string

// Alert severities, least urgent first.
const (
	SeverityWarning  AlertSeverity = "warning"
	SeverityCritical AlertSeverity = "critical"
)

// AlertKind names what an alert is about. Kinds may be added (fields are
// only ever added), so clients show unknown kinds by their title.
type AlertKind string

// Alert kinds the control plane raises.
const (
	// AlertDisk: a server's disk (Docker's data or dock's) is fuller than
	// [alerts] disk_warning_percent or disk_critical_percent.
	AlertDisk AlertKind = "disk"
	// AlertServerOffline: a server has been unreachable longer than
	// [alerts] server_offline_minutes.
	AlertServerOffline AlertKind = "server_offline"
	// AlertCrashLoop: a service's container keeps restarting.
	AlertCrashLoop AlertKind = "crash_loop"
	// AlertDeployFailed: a service's newest release failed; the release
	// before it, if there is one, keeps serving.
	AlertDeployFailed AlertKind = "deploy_failed"
	// AlertDomain: a domain is in error: its certificate wasn't issued
	// although its DNS is right.
	AlertDomain AlertKind = "domain_error"
	// AlertBackupFailed: a backup failed.
	AlertBackupFailed AlertKind = "backup_failed"
	// AlertJobFailed: a scheduled job's run failed or timed out (ADR
	// 0048). Its next successful run resolves it.
	AlertJobFailed AlertKind = "job_failed"
	// AlertNewError: a site's script reported an error of a kind it never
	// had (Dock Analytics, ADR 0037), or a service's logs or traffic
	// brought a new error issue (ADR 0062; Target.ErrorIssue). Resolves
	// itself after a day.
	AlertNewError AlertKind = "new_error"
	// AlertErrorRegressed: an error issue that was resolved, by a person
	// or by itself after 7 days without one, was seen again (ADR 0062;
	// Target.ErrorIssue). Resolves itself after a day, or when the issue
	// is resolved.
	AlertErrorRegressed AlertKind = "error_regressed"
	// AlertErrorSpike: a site's errors in the last five minutes are many
	// times its usual rate.
	AlertErrorSpike AlertKind = "error_spike"
	// AlertTrafficDrop: a site's page views in the last hour fell far
	// below the same hour's usual number.
	AlertTrafficDrop AlertKind = "traffic_drop"
	// AlertUnhealthy: a running service's health check keeps failing
	// (ADR 0043). The control plane restarts it, with backoff; the alert
	// resolves when the check passes again.
	AlertUnhealthy AlertKind = "unhealthy"
	// AlertAccessRequest: a token asks to change a locked environment
	// (ADR 0044); deciding or expiring the request resolves it.
	AlertAccessRequest AlertKind = "access_request"
	// AlertUptimeDown: an uptime check failed twice in a row (ADR 0055).
	// Its next successful probe resolves it.
	AlertUptimeDown AlertKind = "uptime_down"
	// AlertMemory: a server's memory has stayed fuller than [alerts]
	// memory_warning_percent (or memory_critical_percent) for
	// memory_minutes.
	AlertMemory AlertKind = "memory"
	// AlertCPU: a server's CPUs have stayed busier than [alerts]
	// cpu_percent for cpu_minutes.
	AlertCPU AlertKind = "cpu"
	// AlertUpdateFailed: updating Dock, on the control plane or on a
	// server, failed. The next update that succeeds there resolves it.
	AlertUpdateFailed AlertKind = "update_failed"
	// AlertConnection: one of the platform's connections failed when Dock
	// used it: a Cloudflare token refused, the GitHub App's key or
	// installation tokens rejected, backup storage unreachable, the
	// registry's prune or a push failing; or an alert channel got an
	// answer retrying can't change (a removed Slack webhook). The next
	// call that works resolves it.
	AlertConnection AlertKind = "connection_failed"
	// AlertNewVulnerability: a live release's image has critical (or,
	// as the org chose, high) vulnerabilities it didn't have (ADR 0074).
	AlertNewVulnerability AlertKind = "new_vulnerability"
	// AlertCertificate: the wildcard certificate of a base domain can't be
	// issued or renewed (ADR 0090), and the next success resolves it; or
	// a certificate an org uploaded expires within 30 days or has expired
	// (ADR 0110), until it is replaced or removed.
	AlertCertificate AlertKind = "certificate_failed"
	// AlertHostSecurity: a server's host check found something to fix
	// (ADR 0109): one alert per server for its open warning and critical
	// findings (Target.Findings). It resolves when they are fixed or
	// dismissed.
	AlertHostSecurity AlertKind = "host_security"
)

// Alert is one occurrence of a problem, open until its condition clears or
// an admin resolves it. While open it is updated in place (a disk getting
// fuller, the same deploy failing again), never raised twice.
type Alert struct {
	ID       int64         `json:"id"`
	Kind     AlertKind     `json:"kind" doc:"disk, memory, cpu, server_offline, update_failed, connection_failed, crash_loop, deploy_failed, domain_error, backup_failed, new_error, error_regressed, error_spike, traffic_drop, unhealthy, uptime_down, job_failed, access_request, new_vulnerability, certificate_failed, host_security; more may be added"`
	Severity AlertSeverity `json:"severity" enum:"warning,critical"`
	Title    string        `json:"title" example:"Disk 91% full on vps-1"`
	Message  string        `json:"message,omitempty"`
	// Project, Environment, Service and Server say what the alert is
	// about, where that applies.
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
	Service     string `json:"service,omitempty"`
	Server      string `json:"server,omitempty"`
	// Command is the CLI command that shows more, for a copy control;
	// the message doesn't repeat it.
	Command string `json:"command,omitempty" example:"dock release show 8 -p shop -e production -s web"`
	// Target is what the alert points at, for a link to it.
	Target     *AlertTarget `json:"target,omitempty"`
	OpenedAt   time.Time    `json:"opened_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
	ResolvedAt *time.Time   `json:"resolved_at,omitempty"`
	// Count is how many times the condition occurred while the alert was
	// open (ADR 0097): a service that flapped, a deploy that failed again.
	Count int64 `json:"count" example:"1"`
	// ClearedAt is when the alert's condition last cleared; it resolves
	// once it stays clear for [alerts] settle_minutes (ADR 0097).
	ClearedAt *time.Time `json:"cleared_at,omitempty" doc:"When the alert's condition last cleared; it resolves once it stays clear for settle_minutes"`
	// Platform marks the platform's alerts (servers, connections, Dock
	// itself); the rest are the organization's.
	Platform bool `json:"platform,omitempty"`
}

// AlertTarget is what an alert points at, beyond its project,
// environment, service and server: the site and error group of
// new_error, error_spike and traffic_drop, the error issue of new_error
// and error_regressed from a service's errors, the release number of
// deploy_failed, the access request of access_request, the job and run
// of job_failed, the backup run of a service's backup_failed.
type AlertTarget struct {
	Site       string `json:"site,omitempty"`
	ErrorGroup string `json:"error_group,omitempty"`
	Release    int64  `json:"release,omitempty"`
	Request    int64  `json:"request,omitempty"`
	Job        string `json:"job,omitempty"`
	Run        int64  `json:"run,omitempty"`
	Backup     int64  `json:"backup,omitempty"`
	// ErrorIssue is the ID of the error issue a new_error or
	// error_regressed alert is about (GET /errors/{id}).
	ErrorIssue int64 `json:"error_issue,omitempty"`
	// Connection names the connection of connection_failed:
	// cloudflare:<name>, github:<App slug>, storage:<repository>,
	// registry:prune, registry:push and registry:bucket, or
	// alert_channel:<name>.
	Connection string `json:"connection,omitempty"`
	// Image is the image scan of new_vulnerability (GET /org/images/{id}),
	// Vulnerabilities the IDs it is about.
	Image           int64    `json:"image,omitempty"`
	Vulnerabilities []string `json:"vulnerabilities,omitempty"`
	// Findings are the host check findings of host_security (ADR 0109):
	// every one raised while the alert was open, by key.
	Findings []string `json:"findings,omitempty"`
}

// AlertChannelKind is how a channel sends alerts.
type AlertChannelKind string

// Channel kinds.
const (
	// ChannelWebhook POSTs JSON (AlertNotification) signed with
	// HMAC-SHA256 in the Dock-Signature header.
	ChannelWebhook AlertChannelKind = "webhook"
	// ChannelSlack POSTs {"text": ...} to a Slack-compatible incoming
	// webhook (Slack, Mattermost).
	ChannelSlack AlertChannelKind = "slack"
	// ChannelDiscord POSTs {"content": ...} to a Discord webhook, with
	// mentions turned off.
	ChannelDiscord AlertChannelKind = "discord"
	// ChannelPushover sends a push through Pushover's API to a user or
	// group key, with an application's token.
	ChannelPushover AlertChannelKind = "pushover"
	// ChannelEmail sends mail through an SMTP server.
	ChannelEmail AlertChannelKind = "email"
)

// AlertChannel is a configured destination for alerts. Its URL, signing
// secret and SMTP password are stored encrypted and never returned.
type AlertChannel struct {
	Name string           `json:"name"`
	Kind AlertChannelKind `json:"kind" enum:"webhook,slack,discord,pushover,email"`
	// Target is where it sends, without secrets: a URL's scheme and host,
	// or the email recipients.
	Target string `json:"target"`
	// MinSeverity: alerts below it aren't sent here.
	MinSeverity AlertSeverity `json:"min_severity" enum:"warning,critical"`
	// RenotifyMinutes: an alert still open this long after the channel was
	// last told about it is sent again; 0 never.
	RenotifyMinutes int       `json:"renotify_minutes"`
	CreatedAt       time.Time `json:"created_at"`
	// LastSentAt is the last delivery that worked; LastError and
	// LastErrorAt the last that failed after its retries, and Failures
	// how many failed since one worked.
	LastSentAt  *time.Time `json:"last_sent_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	Failures    int        `json:"failures"`
}

// CreateAlertChannelRequest adds a channel.
type CreateAlertChannelRequest struct {
	Name string           `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$"`
	Kind AlertChannelKind `json:"kind" enum:"webhook,slack,discord,pushover,email"`
	// URL is where webhook, slack and discord channels POST (http or
	// https).
	URL string `json:"url,omitempty" maxLength:"2048" doc:"webhook, slack and discord: where to POST. Stored encrypted, never returned"`
	// Secret signs webhook deliveries; when empty one is generated and
	// returned once in CreatedAlertChannel.
	Secret string   `json:"secret,omitempty" maxLength:"256" doc:"webhook: HMAC-SHA256 signing key; generated when empty"`
	To     []string `json:"to,omitempty" maxItems:"20" doc:"email: the recipients; Dock's email server sends (dock email, ADR 0079)"`
	// User and Token are a pushover channel's user (or group) key and
	// application token.
	User  string `json:"user,omitempty" maxLength:"64" doc:"pushover: the user or group key. Stored encrypted, never returned"`
	Token string `json:"token,omitempty" maxLength:"64" doc:"pushover: the application's API token. Stored encrypted, never returned"`
	// MinSeverity defaults to warning (everything).
	MinSeverity AlertSeverity `json:"min_severity,omitempty" enum:"warning,critical"`
	// RenotifyMinutes defaults to 240; 0 sends each alert once.
	RenotifyMinutes *int `json:"renotify_minutes,omitempty" minimum:"0" maximum:"10080" doc:"Default 240; 0 never reminds"`
}

// CreatedAlertChannel is a new channel. Secret is the generated webhook
// signing key, shown only this once.
type CreatedAlertChannel struct {
	Channel AlertChannel `json:"channel"`
	Secret  string       `json:"secret,omitempty"`
	// Warning says why the channel can't send yet: an email channel
	// while Dock's email server isn't set up.
	Warning string `json:"warning,omitempty" doc:"Why the channel can't send yet, such as an email channel while Dock's email isn't set up"`
}

// AlertChannelTest is the outcome of sending a test notification.
type AlertChannelTest struct {
	Channel AlertChannel `json:"channel"`
	OK      bool         `json:"ok"`
	Error   string       `json:"error,omitempty"`
}

// AlertNotification is the JSON body webhook channels receive. Event says
// what happened to the alert: opened, escalated (its severity went up),
// repeated (it happened again while open: another deploy or backup
// failed, ADR 0097), reminder (still open), resolved, or test.
type AlertNotification struct {
	Event  string    `json:"event" enum:"opened,escalated,repeated,reminder,resolved,test"`
	Alert  Alert     `json:"alert"`
	SentAt time.Time `json:"sent_at"`
}
