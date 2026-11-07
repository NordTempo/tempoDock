package apitypes

import "time"

// Draining a server (ADR 0063): every environment on it (or every one of
// a kind) moves to other servers, one at a time, without downtime.

// DrainStatus is where a server drain is.
type DrainStatus string

const (
	DrainRunning   DrainStatus = "running"
	DrainPaused    DrainStatus = "paused"
	DrainSucceeded DrainStatus = "succeeded"
	DrainCancelled DrainStatus = "cancelled"
)

// DrainItemStatus is where one environment of a drain is.
type DrainItemStatus string

const (
	DrainWaiting DrainItemStatus = "waiting"
	DrainMoving  DrainItemStatus = "moving"
	DrainMoved   DrainItemStatus = "moved"
	DrainFailed  DrainItemStatus = "failed"
	DrainSkipped DrainItemStatus = "skipped"
)

// DrainServerRequest drains a server, or plans a drain (DryRun).
type DrainServerRequest struct {
	// Kind limits the drain to one kind of environment ("production" or
	// "non_production"); empty drains every environment. A kind drain
	// stops the server accepting that kind; a full one disables it for
	// placement.
	Kind EnvironmentKind `json:"kind,omitempty" enum:"production,non_production"`
	// Targets pick a project's target server instead of placement's
	// choice. A project's environments of a kind always go together.
	Targets []DrainTarget `json:"targets,omitempty"`
	// MaxHold is each move's hold budget in seconds (default
	// DefaultMaxHold).
	MaxHold int `json:"max_hold,omitempty" minimum:"0" maximum:"600"`
	// DryRun answers with the plan and its problems without changing
	// anything.
	DryRun bool `json:"dry_run,omitempty"`
}

// DrainTarget sends a project's environments to a server.
type DrainTarget struct {
	Project string `json:"project" minLength:"1"`
	To      string `json:"to" minLength:"1"`
}

// ResumeDrainRequest goes on with a paused drain.
type ResumeDrainRequest struct {
	// Skip leaves the environment that failed where it is and goes on
	// with the next; without it the failed one is tried again.
	Skip bool `json:"skip,omitempty"`
}

// ServerDrain is a server's running or latest drain, or a planned one.
type ServerDrain struct {
	// ID is 0 for a plan (DryRun).
	ID     int64           `json:"id"`
	Server string          `json:"server"`
	Kind   EnvironmentKind `json:"kind,omitempty" enum:"production,non_production"`
	// Status is empty for a plan.
	Status DrainStatus `json:"status,omitempty" enum:"running,paused,succeeded,cancelled"`
	Error  string      `json:"error,omitempty"`
	// Items are the environments in the order they move.
	Items []DrainItem `json:"items"`
	// Problems are what keeps a plan from starting, one per environment
	// that can't move as planned.
	Problems []string `json:"problems"`
	// Forwards are the server's forwarded ports: connections that still
	// reach it for services that moved away.
	Forwards   []ServerForward `json:"forwards"`
	MaxHold    int             `json:"max_hold"`
	CreatedBy  string          `json:"created_by,omitempty"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

// DrainItem is one environment of a drain.
type DrainItem struct {
	Project     string          `json:"project"`
	Org         string          `json:"org"`
	Environment string          `json:"environment"`
	Kind        EnvironmentKind `json:"kind" enum:"production,non_production"`
	To          string          `json:"to"`
	Status      DrainItemStatus `json:"status" enum:"waiting,moving,moved,failed,skipped"`
	// Stateful says whether volumes copy; DataBytes is their size, in a
	// plan.
	Stateful  bool  `json:"stateful"`
	DataBytes int64 `json:"data_bytes,omitempty"`
	// Problem keeps a planned item from moving.
	Problem string `json:"problem,omitempty"`
	Error   string `json:"error,omitempty"`
	// Move is its move once it started.
	Move *EnvironmentMove `json:"move,omitempty"`
}

// ServerForward is a port a server forwards for a service that moved
// away from it.
type ServerForward struct {
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Service     string    `json:"service"`
	Port        int64     `json:"port"`
	NowOn       string    `json:"now_on"`
	Since       time.Time `json:"since"`
}

// DrainProgress is how far a server's running or paused drain is.
type DrainProgress struct {
	ID     int64       `json:"id"`
	Status DrainStatus `json:"status" enum:"running,paused"`
	// Done is how many environments moved or were skipped, of Total.
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Error string `json:"error,omitempty"`
}
