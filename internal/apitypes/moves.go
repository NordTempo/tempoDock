package apitypes

import "time"

// MoveStatus is where an environment move is (ADR 0053).
type MoveStatus string

const (
	MoveRunning     MoveStatus = "running"
	MoveRollingBack MoveStatus = "rolling_back"
	MoveSucceeded   MoveStatus = "succeeded"
	MoveFailed      MoveStatus = "failed"
)

// MoveStep is the step a move is on, in order (ADR 0063): prepare
// (images, network, volumes and host ports on the target), sync (volumes
// and snapshots copy in passes while the source serves), stop (the hold
// starts: requests wait, and the environment's services stop on the
// source), copy (the last changes copy and are checked), start (every
// service starts on the target and passes its health check), switch (the
// environment, routes, domains and placement point at the target, and
// the source forwards what still reaches it; the hold ends), cleanup (the
// source's containers go), done.
type MoveStep string

const (
	MovePrepare MoveStep = "prepare"
	MoveSync    MoveStep = "sync"
	MoveStop    MoveStep = "stop"
	MoveCopy    MoveStep = "copy"
	MoveStart   MoveStep = "start"
	MoveSwitch  MoveStep = "switch"
	MoveCleanup MoveStep = "cleanup"
	MoveDone    MoveStep = "done"
)

// MoveEnvironmentRequest moves an environment to another server.
type MoveEnvironmentRequest struct {
	// To is the server's name.
	To string `json:"to" minLength:"1"`
	// MaxHold is the longest the hold may take, in seconds (default
	// DefaultMaxHold): the copy's live passes go on until the last one
	// says the final copy fits, and the move fails without stopping
	// anything if it never does.
	MaxHold int `json:"max_hold,omitempty" minimum:"0" maximum:"600"`
}

// DefaultMaxHold is a move's hold budget when the request sets none, in
// seconds. Caddy holds requests up to a minute (node.HoldFor).
const DefaultMaxHold = 30

// EnvironmentMove is a move of an environment to another server, running
// or the latest one.
type EnvironmentMove struct {
	ID          int64      `json:"id"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	Status      MoveStatus `json:"status" enum:"running,rolling_back,succeeded,failed"`
	Step        MoveStep   `json:"step" enum:"prepare,sync,stop,copy,start,switch,cleanup,done"`
	// Services are the services it starts on the target, stateful first.
	Services []MoveService `json:"services"`
	// DowntimeFrom and DowntimeTo bound the hold: from the source's
	// services stopping until the target serves. Requests wait in Caddy
	// meanwhile, so callers see a slow answer rather than an error.
	DowntimeFrom *time.Time `json:"downtime_from,omitempty"`
	DowntimeTo   *time.Time `json:"downtime_to,omitempty"`
	// MaxHold is the hold budget, in seconds.
	MaxHold int `json:"max_hold"`
	// Drain is the server drain the move is part of, if any.
	Drain int64  `json:"drain,omitempty"`
	Error string `json:"error,omitempty"`
	// Notes are what is left to do by hand: DNS records to change, the
	// source's old volumes to remove once the move checks out.
	Notes      []string   `json:"notes"`
	CreatedBy  string     `json:"created_by"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// MoveService is one service of a move and how far it got.
type MoveService struct {
	Service  string `json:"service"`
	Stateful bool   `json:"stateful"`
	Status   string `json:"status" enum:"waiting,syncing,synced,stopped,copied,started,healthy,failed"`
	// CopiedBytes is how much of its volumes reached the target, every
	// pass together.
	CopiedBytes int64 `json:"copied_bytes"`
	// Passes is how many live passes copied its volumes, and LastPassMS
	// how long the last one took: about what the final copy in the hold
	// takes.
	Passes     int    `json:"passes"`
	LastPassMS int64  `json:"last_pass_ms"`
	Error      string `json:"error,omitempty"`
}
