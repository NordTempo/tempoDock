package apitypes

import "time"

// Scheduled jobs (ADR 0054): named cron schedules on a service, each
// running a command from its active image like `dock run`.

// JobOverlap is what happens when a job comes due while it still runs.
type JobOverlap string

// Overlap policies.
const (
	// OverlapSkip records the due run as skipped (the default).
	OverlapSkip JobOverlap = "skip"
	// OverlapAllow runs it next to the one still running.
	OverlapAllow JobOverlap = "allow"
)

// JobSource says where a job is defined.
type JobSource string

// Job sources.
const (
	// JobFromAPI: set in the UI, CLI or MCP.
	JobFromAPI JobSource = "api"
	// JobFromRepo: from the active release's dock.toml; changed there.
	JobFromRepo JobSource = "repo"
)

// Job limits.
const (
	// KeepJobRuns is how many runs a job's history keeps.
	KeepJobRuns = 20
	// MaxJobsPerService caps a service's jobs.
	MaxJobsPerService = 50
)

// Job is a scheduled job.
type Job struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule" doc:"Cron expression, five fields or a macro such as @daily"`
	Timezone string `json:"timezone" doc:"IANA timezone the schedule is read in"`
	Command  string `json:"command"`
	// TimeoutSeconds stops a run after this long.
	TimeoutSeconds int        `json:"timeout_seconds"`
	Overlap        JobOverlap `json:"overlap" enum:"skip,allow"`
	Enabled        bool       `json:"enabled"`
	Source         JobSource  `json:"source" enum:"api,repo"`
	// NextAt is when it runs next; absent while disabled.
	NextAt *time.Time `json:"next_at,omitempty"`
	// LastRun is its latest run, without output.
	LastRun   *CommandRun `json:"last_run,omitempty"`
	CreatedBy string      `json:"created_by"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// SetJobRequest creates a job or replaces one by name.
type SetJobRequest struct {
	Schedule       string     `json:"schedule" minLength:"1" maxLength:"200" doc:"Cron expression: minute hour day-of-month month day-of-week, e.g. \"0 3 * * *\", or @hourly, @daily, @weekly, @monthly, @yearly"`
	Timezone       string     `json:"timezone,omitempty" maxLength:"64" doc:"IANA timezone, e.g. Europe/Oslo. Default UTC"`
	Command        string     `json:"command" minLength:"1" maxLength:"16384" doc:"Run with /bin/sh -c in the service's active image"`
	TimeoutSeconds int        `json:"timeout_seconds,omitempty" minimum:"0" maximum:"86400" doc:"Default 3600"`
	Overlap        JobOverlap `json:"overlap,omitempty" enum:"skip,allow" doc:"When it comes due while still running: skip (default) or allow"`
	Enabled        *bool      `json:"enabled,omitempty" doc:"Default true"`
}

// SetJobEnabledRequest turns a job on or off.
type SetJobEnabledRequest struct {
	Enabled bool `json:"enabled"`
}
