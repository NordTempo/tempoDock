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
