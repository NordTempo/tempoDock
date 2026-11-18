package apitypes

import "time"

// Template names a database template (ADR 0019): a service created with
// a pinned image, a volume, generated credentials and tuned defaults.
type Template string

const (
	TemplateNone     Template = ""
	TemplatePostgres Template = "postgres"
	TemplateRedis    Template = "redis"
	TemplateMySQL    Template = "mysql"
	TemplateMariaDB  Template = "mariadb"
	TemplateValkey   Template = "valkey"
	// TemplatePgvector is Postgres with the vector extension (pgvector).
	TemplatePgvector Template = "pgvector"
)

// TemplateInfo describes a template for the UI and CLI.
type TemplateInfo struct {
	Name        Template `json:"name" enum:"postgres,redis,mysql,mariadb,valkey,pgvector"`
	Description string   `json:"description"`
	Image       string   `json:"image"`
	MemoryMB    int      `json:"memory_mb"`
	MountPath   string   `json:"mount_path"`
	// Exports are the variables other services reference, as
	// ${{<service>.<name>}}.
	Exports []string `json:"exports"`
}

// BackupKind is how a service is backed up: with the database's own tool
// (postgres, redis, mysql) or file by file (volume).
type BackupKind string

const (
	BackupPostgres BackupKind = "postgres"
	BackupRedis    BackupKind = "redis"
	BackupMySQL    BackupKind = "mysql"
	BackupVolume   BackupKind = "volume"
	// BackupPITR is a Postgres database's base backup for point-in-time
	// restore, and a restore to a time (ADR 0093).
	BackupPITR BackupKind = "pitr"
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
	Kind      BackupKind   `json:"kind" enum:"postgres,redis,mysql,volume,pitr"`
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
	// NotRestorable says why a succeeded backup of a template database
	// can't be restored now, and what to do; empty when it can.
	NotRestorable string `json:"not_restorable,omitempty"`
	// RestoredTo is the service a restore created (or restored in place).
	RestoredTo string `json:"restored_to,omitempty"`
	// ConsistentAt is when a base backup (kind pitr) became consistent:
	// the earliest time it restores to.
	ConsistentAt *time.Time `json:"consistent_at,omitempty"`
	// ToTime is the time a restore to a time restored to.
	ToTime     *time.Time `json:"to_time,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedBy  string     `json:"created_by"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// BackupLog is what a backup or restore run's job printed: its last 16
// KB (MaxRunOutputBytes), with the repository's and database's secrets
// masked. Empty when nothing was kept (the job never ran, or the run is
// older than the service's newest logs); a run that succeeded says so
// instead.
type BackupLog struct {
	ID     int64  `json:"id"`
	Output string `json:"output"`
}

// BackupSchedule is when a service is backed up.
type BackupSchedule struct {
	Enabled bool `json:"enabled"`
	// Hour is the hour of day (0-23, the control plane's local time) of
	// the daily backup; DefaultHour says it follows the install's.
	Hour        int  `json:"hour"`
	DefaultHour bool `json:"default_hour"`
	// NextAt is the next scheduled backup, if enabled, in the control
	// plane's time zone like Hour.
	NextAt *time.Time `json:"next_at,omitempty"`
}

// ServiceBackups is a stateful service's backups: how, when, and the runs.
type ServiceBackups struct {
	Service string     `json:"service"`
	Kind    BackupKind `json:"kind" enum:"postgres,redis,mysql,volume"`
	// Warning explains the limits of file-level volume backups.
	Warning string `json:"warning,omitempty"`
	// Restorable says backups can be restored into a new service (template
	// databases).
	Restorable bool           `json:"restorable"`
	OffBox     bool           `json:"off_box"`
	Schedule   BackupSchedule `json:"schedule"`
	// PITR is point-in-time restore, for Postgres template databases.
	PITR *PITR `json:"pitr,omitempty"`
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

// RestoreBackupRequest restores a snapshot, or the database as it was
// at a time, into a new service, or a time in place.
type RestoreBackupRequest struct {
	// Snapshot is a snapshot ID or its first 8 or more characters.
	Snapshot string `json:"snapshot,omitempty"`
	// ToTime restores the database as it was at that moment, from its
	// point-in-time archive (ADR 0093), instead of a snapshot.
	ToTime *time.Time `json:"to_time,omitempty"`
	// As names the new service; empty is <service>-restored.
	As string `json:"as,omitempty"`
	// InPlace restores ToTime into the database itself: it stops, a
	// safety snapshot keeps what it holds, and it starts on the restored
	// data. Not with As.
	InPlace bool `json:"in_place,omitempty"`
}

// PITR is a Postgres database's point-in-time restore (ADR 0093).
type PITR struct {
	Enabled bool `json:"enabled"`
	// Days is how long the restorable window is kept.
	Days int `json:"days"`
	// Unavailable says why it can't be turned on, and what to do; empty
	// when it can.
	Unavailable string `json:"unavailable,omitempty"`
	// From and Through are the restorable window: any time between them
	// restores. Nil while nothing does yet.
	From    *time.Time `json:"from,omitempty"`
	Through *time.Time `json:"through,omitempty"`
	// BaseBackups counts the base backups the window rests on.
	BaseBackups int `json:"base_backups"`
	// WALBytes is the archive's size when retention last measured it.
	WALBytes int64 `json:"wal_bytes"`
	// UsedBy are the services whose variables reference the database.
	UsedBy []string `json:"used_by"`
	// Deploys are their releases that went live in the window, newest
	// first (at most 50), to find the moment before one.
	Deploys []PITRDeploy `json:"deploys"`
	// Archiver is how archiving does, while it is on.
	Archiver *PITRArchiver `json:"archiver,omitempty"`
}

// PITRDeploy is a release of a service using the database that went live.
type PITRDeploy struct {
	Service string    `json:"service"`
	Number  int64     `json:"number"`
	At      time.Time `json:"at"`
}

// PITRArchiver is what a database's WAL archiver reports.
type PITRArchiver struct {
	Healthy bool `json:"healthy"`
	// Problem says what is wrong when it isn't healthy.
	Problem string `json:"problem,omitempty"`
	// Waiting are the finished WAL files waiting to be archived, their
	// size and when the oldest finished.
	Waiting       int        `json:"waiting"`
	WaitingBytes  int64      `json:"waiting_bytes"`
	OldestWaiting *time.Time `json:"oldest_waiting,omitempty"`
	// DiskBytes and DiskFree are the database volume's disk.
	DiskBytes   int64      `json:"disk_bytes,omitempty"`
	DiskFree    int64      `json:"disk_free,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
}

// SetPITRRequest turns point-in-time restore on or off, or changes how
// many days it keeps. Unset fields stay as they are.
type SetPITRRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
	Days    *int  `json:"days,omitempty" minimum:"1" maximum:"35"`
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
	Hour int `json:"hour"`
	// TimeZone names the control plane's zone: Europe/Oslo, or UTC.
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
