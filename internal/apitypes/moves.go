package apitypes

import "time"

// MoveStatus is where an environment move is (ADR 0052).
type MoveStatus string

const (
	MoveRunning     MoveStatus = "running"
	MoveRollingBack MoveStatus = "rolling_back"
	MoveSucceeded   MoveStatus = "succeeded"
	MoveFailed      MoveStatus = "failed"
)

// MoveStep is the step a move is on, in order: prepare (images, network,
// volumes and host ports on the target), stop (the stateful services stop
// on the source), copy (their volumes stream to the target), start
// (every service starts on the target and passes its health check),
// switch (the environment, routes, domains and placement point at the
// target), cleanup (the source's containers go), done.
type MoveStep string

const (
	MovePrepare MoveStep = "prepare"
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
}

// EnvironmentMove is a move of an environment to another server, running
// or the latest one.
type EnvironmentMove struct {
	ID          int64      `json:"id"`
	Project     string     `json:"project"`
	Environment string     `json:"environment"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	Status      MoveStatus `json:"status" enum:"running,rolling_back,succeeded,failed"`
	Step        MoveStep   `json:"step" enum:"prepare,stop,copy,start,switch,cleanup,done"`
	// Services are the services it starts on the target, stateful first.
	Services []MoveService `json:"services"`
	// DowntimeFrom and DowntimeTo bound the stateful services' downtime:
	// stopped on the source until healthy on the target.
	DowntimeFrom *time.Time `json:"downtime_from,omitempty"`
	DowntimeTo   *time.Time `json:"downtime_to,omitempty"`
	Error        string     `json:"error,omitempty"`
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
	Status   string `json:"status" enum:"waiting,stopped,copied,started,healthy,failed"`
	// CopiedBytes is how much of its volumes reached the target.
	CopiedBytes int64  `json:"copied_bytes"`
	Error       string `json:"error,omitempty"`
}
