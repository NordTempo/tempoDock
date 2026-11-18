package apitypes

import "time"

// RecoveryState says what the recovery plan does with a service.
type RecoveryState string

const (
	// RecoveryRestore: the service has a backup to restore.
	RecoveryRestore RecoveryState = "restore"
	// RecoveryLost: its backups were only in a server's local repository,
	// which went with the server.
	RecoveryLost RecoveryState = "lost"
	// RecoveryNone: it was never backed up.
	RecoveryNone RecoveryState = "none"
)

// RecoveryService is one stateful service in a recovery plan.
type RecoveryService struct {
	Org         string `json:"org"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	// Kind is how it is backed up: a template database's dump, or its
	// volumes.
	Kind  BackupKind    `json:"kind"`
	State RecoveryState `json:"state"`
	// Snapshot, SizeBytes and BackedUpAt describe the backup a recovery
	// restores; empty unless State is restore.
	Snapshot   string     `json:"snapshot,omitempty"`
	SizeBytes  int64      `json:"size_bytes,omitempty"`
	BackedUpAt *time.Time `json:"backed_up_at,omitempty"`
}

// RecoveryPlan is what `dock admin recover` restores on a control plane
// in recovery mode (ADR 0098): the stateful services of its own server.
type RecoveryPlan struct {
	// Since is when the control plane was restored, and From the archive
	// it came from.
	Since time.Time `json:"since"`
	From  string    `json:"from"`
	// Repository is where the backups are read from ("local": on the
	// server, so gone with the old one).
	Repository string            `json:"repository"`
	Services   []RecoveryService `json:"services"`
}

// RecoveryOutcome is what a recovery did with one service.
type RecoveryOutcome string

const (
	// RecoveryRestored: its data came back from a backup and it runs.
	RecoveryRestored RecoveryOutcome = "restored"
	// RecoveryEmpty: a stateful service without a backup to restore; it
	// runs on empty volumes.
	RecoveryEmpty RecoveryOutcome = "empty"
	// RecoveryStarted: a service without volumes; it runs.
	RecoveryStarted RecoveryOutcome = "started"
	// RecoveryRunning: it ran already and was left alone.
	RecoveryRunning RecoveryOutcome = "running"
	// RecoverySkipped: nothing to start (stopped on purpose, asleep).
	RecoverySkipped RecoveryOutcome = "skipped"
	// RecoveryFailed: the restore or the start failed; Detail says which.
	RecoveryFailed RecoveryOutcome = "failed"
)

// RecoveryResult is one service in a recovery's report.
type RecoveryResult struct {
	Org         string          `json:"org"`
	Project     string          `json:"project"`
	Environment string          `json:"environment"`
	Service     string          `json:"service"`
	Outcome     RecoveryOutcome `json:"outcome"`
	// Detail says why for skipped and failed, and what to do next.
	Detail string `json:"detail,omitempty"`
	// Snapshot and BackedUpAt are the backup that was restored.
	Snapshot   string     `json:"snapshot,omitempty"`
	BackedUpAt *time.Time `json:"backed_up_at,omitempty"`
}

// RecoveryReport is how a recovery's run goes or went: one result per
// service handled so far, in the order they were handled.
type RecoveryReport struct {
	// Running is true until the run has handled every service.
	Running    bool             `json:"running"`
	StartedAt  *time.Time       `json:"started_at,omitempty"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
	Results    []RecoveryResult `json:"results"`
}
