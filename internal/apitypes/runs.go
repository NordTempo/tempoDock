package apitypes

import "time"

// One-off commands in a service's active image (`dock run`, ADR 0048).

// RunProtocol is the Upgrade token of a streamed command: `GET
// .../run` with `Connection: Upgrade` and `Upgrade: dock-run`, then
// runframe frames both ways.
const RunProtocol = "dock-run"

// RunVolumes is how a command gets its service's volumes.
type RunVolumes string

// Volume modes. A database template's volumes are never mounted; another
// stateful service's are read-only unless the caller asks for write.
const (
	VolumesNone  RunVolumes = "none"
	VolumesRead  RunVolumes = "read"
	VolumesWrite RunVolumes = "write"
)

// RunStatus is where a run is.
type RunStatus string

// Run statuses. Every status but running is final.
const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	// RunFailed: it exited non-zero (ExitCode), or couldn't run (Error).
	RunFailed RunStatus = "failed"
	// RunTimedOut: it ran past its timeout and was stopped.
	RunTimedOut RunStatus = "timed_out"
	// RunCanceled: its caller went away (a closed `dock run`).
	RunCanceled RunStatus = "canceled"
	// RunInterrupted: the control plane stopped or restarted while it ran.
	RunInterrupted RunStatus = "interrupted"
	// RunSkipped: a job's scheduled run didn't start (still running, no
	// active release, the service stopped); Error says why.
	RunSkipped RunStatus = "skipped"
)

// RunTrigger says what started a run.
type RunTrigger string

// Run triggers.
const (
	// TriggerCommand: someone ran a command (`dock run`, the UI, MCP).
	TriggerCommand RunTrigger = "command"
	// TriggerSchedule: a job came due (ADR 0052).
	TriggerSchedule RunTrigger = "schedule"
	// TriggerManual: someone ran a job now.
	TriggerManual RunTrigger = "manual"
)

// Command limits.
const (
	// MaxCommandBytes caps a command's length.
	MaxCommandBytes = 16 << 10
	// DefaultRunTimeoutSeconds is a run's timeout when none is given, and
	// MaxRunTimeoutSeconds the longest one may ask for.
	DefaultRunTimeoutSeconds = 3600
	MaxRunTimeoutSeconds     = 86400
	// MaxRunOutputBytes caps the output tail a run keeps.
	MaxRunOutputBytes = 16 << 10
)

// RunCommandRequest runs a one-off command. The streamed form (`GET
// .../run`, RunProtocol) takes the same fields as query parameters, plus
// tty, stdin, cols and rows.
type RunCommandRequest struct {
	Command        string     `json:"command" minLength:"1" maxLength:"16384" doc:"Run with /bin/sh -c in the service's active image, its variables and its environment's private network"`
	TimeoutSeconds int        `json:"timeout_seconds,omitempty" minimum:"0" maximum:"86400" doc:"Stopped after this long. Default 3600"`
	Volumes        RunVolumes `json:"volumes,omitempty" enum:"none,read,write" doc:"The service's volumes: read-only by default on a stateful service, none on a database template (never mounted) or a stateless service. write mounts them read-write next to the running service: only for apps whose storage takes a second writer"`
}

// CommandRun is one run of a command.
type CommandRun struct {
	ID          int64      `json:"id"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	Service     string     `json:"service"`
	Trigger     RunTrigger `json:"trigger" enum:"command,schedule,manual"`
	// Job is the job a scheduled or run-now run belongs to.
	Job     string `json:"job,omitempty"`
	Command string `json:"command"`
	// Release is the number of the release it ran in; 0 when there was
	// none (a scheduled run skipped for it).
	Release        int64      `json:"release"`
	TTY            bool       `json:"tty"`
	Volumes        RunVolumes `json:"volumes" enum:"none,read,write"`
	TimeoutSeconds int        `json:"timeout_seconds"`
	Status         RunStatus  `json:"status" enum:"running,succeeded,failed,timed_out,canceled,interrupted,skipped"`
	// ExitCode is set once it exited by itself.
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
	// Output is the tail of its output (at most MaxRunOutputBytes); never
	// kept for terminal sessions, and left out of lists.
	Output     string     `json:"output,omitempty"`
	CreatedBy  string     `json:"created_by"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}
