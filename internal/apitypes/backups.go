package apitypes

import "time"

// Template names a database template (ADR 0019): a service created with
// a pinned image, a volume, generated credentials and tuned defaults.
type Template string

const (
	TemplateNone     Template = ""
	TemplatePostgres Template = "postgres"
	TemplateRedis    Template = "redis"
)

// TemplateInfo describes a template for the UI and CLI.
type TemplateInfo struct {
	Name        Template `json:"name" enum:"postgres,redis"`
	Description string   `json:"description"`
	Image       string   `json:"image"`
	MemoryMB    int      `json:"memory_mb"`
	MountPath   string   `json:"mount_path"`
	// Exports are the variables other services reference, as
	// ${{<service>.<name>}}.
	Exports []string `json:"exports"`
}

// BackupKind is how a service is backed up: with the database's own tool
// (postgres, redis) or file by file (volume).
type BackupKind string

const (
	BackupPostgres BackupKind = "postgres"
	BackupRedis    BackupKind = "redis"
	BackupVolume   BackupKind = "volume"
)

// BackupStatus is where a backup or restore run is.
type BackupStatus string

const (
	BackupRunning   BackupStatus = "running"
	BackupSucceeded BackupStatus = "succeeded"
	BackupFailed    BackupStatus = "failed"
)

// BackupRun is one backup, or one restore from a backup.
type BackupRun struct {
	ID        int64        `json:"id"`
	Service   string       `json:"service"`
	Operation string       `json:"operation" enum:"backup,restore"`
	Kind      BackupKind   `json:"kind" enum:"postgres,redis,volume"`
	Trigger   string       `json:"trigger" enum:"schedule,manual"`
	Status    BackupStatus `json:"status" enum:"running,succeeded,failed"`
	// Snapshot is the restic snapshot ID: written by a backup, read by a
	// restore.
	Snapshot  string `json:"snapshot,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	// Repository is where the snapshot is: "local" (on the service's
	// server, not off-box) or the S3 repository URL.
	Repository string `json:"repository"`
	OffBox     bool   `json:"off_box"`
	// Pruned snapshots were removed by retention and can't be restored.
	Pruned bool `json:"pruned,omitempty"`
	// RestoredTo is the service a restore created.
	RestoredTo string     `json:"restored_to,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedBy  string     `json:"created_by"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// BackupSchedule is when a service is backed up.
type BackupSchedule struct {
	Enabled bool `json:"enabled"`
	// Hour is the hour of day (0-23, the control plane's local time) of
	// the daily backup; DefaultHour says it follows the install's.
	Hour        int  `json:"hour"`
	DefaultHour bool `json:"default_hour"`
	// NextAt is the next scheduled backup, if enabled.
	NextAt *time.Time `json:"next_at,omitempty"`
}

// ServiceBackups is a stateful service's backups: how, when, and the runs.
type ServiceBackups struct {
	Service string     `json:"service"`
	Kind    BackupKind `json:"kind" enum:"postgres,redis,volume"`
	// Warning explains the limits of file-level volume backups.
	Warning string `json:"warning,omitempty"`
	// Restorable says backups can be restored into a new service (template
	// databases).
	Restorable bool           `json:"restorable"`
	OffBox     bool           `json:"off_box"`
	Schedule   BackupSchedule `json:"schedule"`
	// Runs are the latest backups and restores, newest first.
	Runs []BackupRun `json:"runs"`
}

// UpdateBackupScheduleRequest changes a service's schedule. Fields that
// aren't set stay as they are; default_hour goes back to the install's.
type UpdateBackupScheduleRequest struct {
	Enabled     *bool `json:"enabled,omitempty"`
	Hour        *int  `json:"hour,omitempty" minimum:"0" maximum:"23"`
	DefaultHour bool  `json:"default_hour,omitempty"`
}

// RestoreBackupRequest restores a snapshot into a new service.
type RestoreBackupRequest struct {
	// Snapshot is a snapshot ID or its first 8 or more characters.
	Snapshot string `json:"snapshot" minLength:"8"`
	// As names the new service; empty is <service>-restored.
	As string `json:"as,omitempty"`
}

// RestoreResult is the new service a restore creates, and the restore run
// filling it in the background.
type RestoreResult struct {
	Service Service   `json:"service"`
	Restore BackupRun `json:"restore"`
}

// BackupSettings are the install's backup settings.
type BackupSettings struct {
	// Repository is the S3 repository URL backups go to; empty keeps them
	// in a local repository on each server (not off-box).
	Repository string `json:"repository"`
	// Storage is the storage connection backups go to, and Path the folder
	// in its bucket; empty for local repositories (ADR 0039).
	Storage string `json:"storage,omitempty"`
	Path    string `json:"path,omitempty"`
	// Repositories are where the backups restores can use are now: each
	// local repository by server, and the bucket's.
	Repositories []BackupRepository `json:"repositories"`
	// Move is the latest move of the backups to other storage since the
	// daemon started.
	Move           *BackupMove `json:"move,omitempty"`
	OffBox         bool        `json:"off_box"`
	HasCredentials bool        `json:"has_credentials"`
	KeepDaily      int         `json:"keep_daily"`
	KeepWeekly     int         `json:"keep_weekly"`
	KeepMonthly    int         `json:"keep_monthly"`
	// Hour is when scheduled backups run (0-23, the control plane's local
	// time) unless a service sets its own.
	Hour        int        `json:"hour"`
	TimeZone    string     `json:"time_zone"`
	ResticImage string     `json:"restic_image"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	UpdatedBy   string     `json:"updated_by,omitempty"`
	// Password is the restic repository password: in the answer that
	// generated it, and to admins who ask (reveal). Without it the backups
	// can't be read.
	Password string `json:"password,omitempty"`
	// ControlPlane is the control plane's own daily backup (ADR 0021).
	ControlPlane ControlPlaneBackup `json:"control_plane"`
}

// BackupRepository is one repository holding backups restores can use.
type BackupRepository struct {
	// Repository is "local" or the S3 repository URL.
	Repository string `json:"repository"`
	// Server holds a local repository; empty for a bucket.
	Server string `json:"server,omitempty"`
	// Current: new backups go here.
	Current   bool  `json:"current"`
	Snapshots int   `json:"snapshots"`
	Bytes     int64 `json:"bytes"`
}

// BackupMove is a move of the backups to a storage connection (`dock
// backup move`): each source repository is copied into the bucket,
// checked, the backups are pointed at their copies, and the source is
// emptied. Backups and restores started while it runs are refused;
// scheduled ones wait for it.
type BackupMove struct {
	// To is the storage connection, Path the folder in its bucket.
	To      string             `json:"to"`
	Path    string             `json:"path"`
	Running bool               `json:"running"`
	Sources []BackupMoveSource `json:"sources"`
	// Moved is how many backups now point at their copies; Gone how many
	// were missing from their repository already (now marked pruned).
	Moved      int        `json:"moved"`
	Gone       int        `json:"gone"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// BackupMoveSource is one repository a move copies from.
type BackupMoveSource struct {
	// Repository is "local" or the S3 repository URL.
	Repository string `json:"repository"`
	// Server holds the local repository; empty for a bucket.
	Server string `json:"server,omitempty"`
	Status string `json:"status" enum:"waiting,copying,copied,emptied,failed"`
	// Error says why it failed, or why it couldn't be emptied.
	Error string `json:"error,omitempty"`
}

// MoveBackupsRequest moves the backups into a storage connection's bucket.
type MoveBackupsRequest struct {
	Storage string `json:"storage" doc:"the storage connection"`
	Path    string `json:"path,omitempty" doc:"folder in the bucket; default dock-backups"`
}

// ControlPlaneBackup is the control plane's own backup into the
// repository: its database, keys and config, daily at the install's hour.
type ControlPlaneBackup struct {
	NextAt *time.Time `json:"next_at,omitempty"`
	// Latest is the latest run, if any.
	Latest *ControlPlaneBackupRun `json:"latest,omitempty"`
}

// ControlPlaneBackupRun is one backup of the control plane.
type ControlPlaneBackupRun struct {
	Trigger    string       `json:"trigger" enum:"schedule,manual"`
	Status     BackupStatus `json:"status" enum:"running,succeeded,failed"`
	Snapshot   string       `json:"snapshot,omitempty"`
	SizeBytes  int64        `json:"size_bytes"`
	Repository string       `json:"repository"`
	OffBox     bool         `json:"off_box"`
	Error      string       `json:"error,omitempty"`
	CreatedBy  string       `json:"created_by"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
}

// ConfigureBackupRequest changes the install's backup retention and hour.
// Fields that aren't set stay as they are.
type ConfigureBackupRequest struct {
	// Repository and the S3 keys are refused since ADR 0039: buckets are
	// storage connections, and `dock backup move` takes backups there.
	Repository      *string `json:"repository,omitempty" doc:"no longer settable: add the bucket as a storage connection and move the backups there (POST /backup/move)"`
	AccessKeyID     *string `json:"access_key_id,omitempty" doc:"no longer settable, see repository"`
	SecretAccessKey *string `json:"secret_access_key,omitempty" doc:"no longer settable, see repository"`
	Region          *string `json:"region,omitempty" doc:"no longer settable, see repository"`
	KeepDaily       *int    `json:"keep_daily,omitempty" minimum:"1" maximum:"365"`
	KeepWeekly      *int    `json:"keep_weekly,omitempty" minimum:"0" maximum:"520"`
	KeepMonthly     *int    `json:"keep_monthly,omitempty" minimum:"0" maximum:"240"`
	Hour            *int    `json:"hour,omitempty" minimum:"0" maximum:"23"`
}
