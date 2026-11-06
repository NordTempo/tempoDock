package apitypes

import "time"

// SnapshotTrigger is why a volume snapshot was taken (ADR 0047).
type SnapshotTrigger string

const (
	// SnapshotManual is `dock volume snapshot`, the UI or an agent.
	SnapshotManual SnapshotTrigger = "manual"
	// SnapshotBeforeRestore is taken of the data an in-place restore
	// replaces.
	SnapshotBeforeRestore SnapshotTrigger = "before_restore"
	// SnapshotBeforeDeploy is taken before a deploy's pre-deploy command.
	SnapshotBeforeDeploy SnapshotTrigger = "before_deploy"
	// SnapshotBeforeRun is taken before `dock run` on a stateful service.
	SnapshotBeforeRun SnapshotTrigger = "before_run"
	// SnapshotBeforeMove is taken before an environment moves to another
	// server.
	SnapshotBeforeMove SnapshotTrigger = "before_move"
)

// SnapshotStatus is where a snapshot is.
type SnapshotStatus string

const (
	SnapshotRunning SnapshotStatus = "running"
	SnapshotReady   SnapshotStatus = "ready"
	SnapshotFailed  SnapshotStatus = "failed"
)

// Snapshot is a copy of a service's volumes on its server, taken in one
// moment (ADR 0047).
type Snapshot struct {
	ID      int64           `json:"id"`
	Service string          `json:"service"`
	Trigger SnapshotTrigger `json:"trigger" enum:"manual,before_restore,before_deploy,before_run,before_move"`
	Status  SnapshotStatus  `json:"status" enum:"running,ready,failed"`
	// Server is where the snapshot's files are.
	Server string `json:"server"`
	// Volumes are the service's volumes it holds, by name.
	Volumes   []string `json:"volumes"`
	SizeBytes int64    `json:"size_bytes"`
	// PausedMS is how long the service was paused for the copy.
	PausedMS   int64      `json:"paused_ms"`
	Note       string     `json:"note,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedBy  string     `json:"created_by"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// SnapshotRestore is one restore of a snapshot, into a new service or in
// place.
type SnapshotRestore struct {
	ID int64 `json:"id"`
	// Snapshot is the snapshot restored; 0 once it was deleted.
	Snapshot int64 `json:"snapshot"`
	// Service is the service the snapshot is of; To the one restored into
	// (the same one in place).
	Service string `json:"service"`
	To      string `json:"to"`
	InPlace bool   `json:"in_place"`
	// Safety is the snapshot an in-place restore took of the data it
	// replaced; restore it to undo.
	Safety     int64        `json:"safety,omitempty"`
	Status     BackupStatus `json:"status" enum:"running,succeeded,failed"`
	Error      string       `json:"error,omitempty"`
	CreatedBy  string       `json:"created_by"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
}

// ServiceSnapshots is a stateful service's snapshots and restores.
type ServiceSnapshots struct {
	Service string `json:"service"`
	// IntoNew says snapshots restore into a new service (template
	// databases); every service restores in place.
	IntoNew bool `json:"into_new"`
	// Keep and KeepDays are retention: the newest Keep snapshots of each
	// service are kept, and none older than KeepDays.
	Keep      int        `json:"keep"`
	KeepDays  int        `json:"keep_days"`
	Snapshots []Snapshot `json:"snapshots"`
	// Restores are the latest restores, newest first.
	Restores []SnapshotRestore `json:"restores"`
}

// CreateSnapshotRequest takes a snapshot.
type CreateSnapshotRequest struct {
	// Note says what it is for, e.g. "before the 2.0 migration".
	Note string `json:"note,omitempty" maxLength:"200"`
}

// RestoreSnapshotRequest restores a snapshot. By default a template
// database restores into a new service (As names it, default
// <service>-restored), leaving the service as it is. InPlace
// replaces the service's own volumes instead: it is stopped, its current
// data is snapshotted first (the restore's safety snapshot), the
// snapshot's data is copied in, and it starts again. Services that aren't
// template databases restore in place only.
type RestoreSnapshotRequest struct {
	As      string `json:"as,omitempty"`
	InPlace bool   `json:"in_place,omitempty"`
}

// SnapshotRestoreResult is a started restore, and the new service for a
// restore into one.
type SnapshotRestoreResult struct {
	Restore SnapshotRestore `json:"restore"`
	Service *Service        `json:"service,omitempty"`
}
