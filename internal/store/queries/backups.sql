-- name: SetServiceTemplate :exec
UPDATE services SET template = ? WHERE id = +?;

-- name: GetBackupSettings :one
SELECT * FROM backup_settings WHERE id = 1;

-- name: UpsertBackupSettings :one
INSERT INTO backup_settings (id, repository, credentials, password, keep_daily, keep_weekly, keep_monthly, hour, updated_at, updated_by, storage_id, storage_path)
VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    repository = excluded.repository, credentials = excluded.credentials, password = excluded.password,
    keep_daily = excluded.keep_daily, keep_weekly = excluded.keep_weekly, keep_monthly = excluded.keep_monthly,
    hour = excluded.hour, updated_at = excluded.updated_at, updated_by = excluded.updated_by,
    storage_id = excluded.storage_id, storage_path = excluded.storage_path
RETURNING *;

-- name: CreateBackupPassword :exec
-- Creates the settings row with only a password (the rest default) unless
-- it exists: a caller holding the defaults never overwrites settings
-- written meanwhile.
INSERT INTO backup_settings (id, password, updated_at) VALUES (1, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: GetBackupSchedule :one
SELECT * FROM backup_schedules WHERE service_id = +?;

-- name: UpsertBackupSchedule :exec
INSERT INTO backup_schedules (service_id, enabled, hour) VALUES (?, ?, ?)
ON CONFLICT (service_id) DO UPDATE SET enabled = excluded.enabled, hour = excluded.hour;

-- name: ListBackupCandidates :many
-- Every live stateful service (an applied volume, not archived or stopped,
-- in a live project) with its server and schedule override, for the
-- scheduler. A stopped service changes nothing to back up (ADR 0050).
-- Preview environments' services are never backed up on a schedule: their
-- data is thrown away when the pull request closes (ADR 0056).
SELECT services.id, services.created_at, environments.server_id,
       CAST(coalesce(backup_schedules.enabled, 1) AS BOOLEAN) AS enabled,
       backup_schedules.hour
FROM services
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
LEFT JOIN backup_schedules ON backup_schedules.service_id = services.id
WHERE services.archived_at IS NULL AND services.stopped_at IS NULL AND projects.archived_at IS NULL
  AND EXISTS (SELECT 1 FROM volumes WHERE volumes.service_id = services.id AND volumes.pending = 0)
  AND NOT EXISTS (SELECT 1 FROM previews WHERE previews.environment_id = services.environment_id)
ORDER BY services.id;

-- name: ListLatestScheduledBackups :many
-- Each service's latest scheduled backup attempt.
SELECT * FROM backups WHERE id IN (
    SELECT max(id) FROM backups WHERE operation = 'backup' AND trigger = 'schedule' GROUP BY service_id
);

-- name: ListProjectLatestBackups :many
-- Each service's latest backup run in a project, for the overview.
SELECT * FROM backups WHERE id IN (
    SELECT max(backups.id) FROM backups
    JOIN services ON services.id = backups.service_id
    JOIN environments ON environments.id = services.environment_id
    WHERE environments.project_id = +? AND backups.operation = 'backup'
    GROUP BY backups.service_id
);

-- name: CreateBackup :one
INSERT INTO backups (service_id, operation, kind, trigger, status, repository, snapshot_id, restored_to, created_by, started_at)
VALUES (?, ?, ?, ?, 'running', ?, ?, ?, ?, ?)
RETURNING *;

-- name: FinishBackup :exec
UPDATE backups SET status = ?, snapshot_id = ?, size_bytes = ?, error = ?, finished_at = ? WHERE id = +?;

-- name: GetBackup :one
SELECT * FROM backups WHERE id = +?;

-- name: GetBackupBySnapshot :one
-- A service's succeeded backup by snapshot ID or a prefix of it (restic's
-- short IDs are 8 characters).
SELECT * FROM backups
WHERE service_id = +sqlc.arg(service_id) AND operation = 'backup' AND status = 'succeeded'
  AND substr(snapshot_id, 1, length(sqlc.arg(prefix))) = sqlc.arg(prefix)
ORDER BY id DESC;

-- name: ListServiceBackups :many
SELECT * FROM backups WHERE service_id = +? ORDER BY id DESC LIMIT +?;

-- name: ListUnprunedBackups :many
-- A service's succeeded backups in one repository that retention hasn't
-- removed yet.
SELECT * FROM backups
WHERE service_id = +? AND repository = +? AND operation = 'backup' AND status = 'succeeded' AND pruned = 0
ORDER BY id;

-- name: PruneBackup :exec
UPDATE backups SET pruned = 1 WHERE id = +?;

-- name: ListRunningRestoresInto :many
-- Restores into a new service still marked running. Read once at
-- start-up, before FailRunningBackups fails them: a restart cut them
-- short, and their targets are archived like any failed restore's
-- (finishCutRestores). A scan of the running rows, as the two below.
SELECT * FROM backups
WHERE status = 'running' AND operation = 'restore' AND restored_to IS NOT NULL AND restored_to <> service_id;

-- name: FailRunningInPlaceRestores :execrows
-- In-place restores a restart cut short (a volume backup's, or to a
-- time): the service is its own target. Run before FailRunningBackups,
-- with what to do about the data. Once at start-up: a scan of the running
-- rows.
UPDATE backups SET status = 'failed', error = ?, finished_at = ?
WHERE status = 'running' AND operation = 'restore' AND restored_to = service_id;

-- name: FailRunningBackups :execrows
-- Runs a restart cut short.
UPDATE backups SET status = 'failed', error = ?, finished_at = ? WHERE status = 'running';

-- name: ListBackupRepositories :many
-- Where the backups restores can still use are: succeeded, unpruned
-- backups by repository and the server their service is on, with their
-- count and size. For the Backups page; the table holds a few rows per
-- service and day, so a scan is fine.
SELECT backups.repository, servers.name AS server, CAST(count(*) AS INTEGER) AS snapshots,
       CAST(coalesce(sum(backups.size_bytes), 0) AS INTEGER) AS size_bytes
FROM backups
JOIN services ON services.id = backups.service_id
JOIN environments ON environments.id = services.environment_id
JOIN servers ON servers.id = environments.server_id
WHERE backups.operation = 'backup' AND backups.status = 'succeeded' AND backups.pruned = 0
GROUP BY backups.repository, servers.name
ORDER BY backups.repository, servers.name;

-- name: ListLocalBackupServers :many
-- The servers holding a local repository with backups restores can still
-- use: where a backup move copies from.
SELECT DISTINCT environments.server_id
FROM backups
JOIN services ON services.id = backups.service_id
JOIN environments ON environments.id = services.environment_id
WHERE backups.repository = '' AND backups.operation = 'backup' AND backups.status = 'succeeded' AND backups.pruned = 0;

-- name: MoveBackupSnapshot :exec
-- Points a backup at its copy in another repository (a backup move).
UPDATE backups SET repository = ?, snapshot_id = ? WHERE repository = +? AND snapshot_id = +?;

-- name: RelabelBackupRepository :exec
-- Renames a repository in the backups' history (an old repository URL
-- converted to a storage connection).
UPDATE backups SET repository = ? WHERE repository = +?;

-- name: ListRepositorySnapshots :many
-- The snapshots of one repository that backups restores can use point at.
SELECT snapshot_id FROM backups
WHERE repository = +? AND operation = 'backup' AND status = 'succeeded' AND pruned = 0 AND snapshot_id <> '';

-- name: PruneBackupSnapshot :exec
-- Marks the backups of a snapshot gone: a move found it in no repository.
UPDATE backups SET pruned = 1 WHERE repository = +? AND snapshot_id = +?;

-- name: SaveBackupLog :exec
-- Keeps a finished run's output (the run's service: a restore's is the
-- service restored from).
INSERT INTO backup_logs (backup_id, service_id, output) VALUES (?, ?, ?)
ON CONFLICT (backup_id) DO UPDATE SET output = excluded.output;

-- name: GetBackupLog :one
-- A run's output; the service guards IDs from other services.
SELECT output FROM backup_logs WHERE backup_id = +? AND service_id = +?;

-- name: TrimBackupLogs :exec
-- Deletes the output of a service's runs older than its newest `keep`
-- logs.
DELETE FROM backup_logs
WHERE backup_logs.service_id = +sqlc.arg(service_id) AND backup_logs.backup_id < (
    SELECT l.backup_id FROM backup_logs l WHERE l.service_id = +sqlc.arg(service_id)
    ORDER BY l.backup_id DESC LIMIT 1 OFFSET CAST(sqlc.arg(keep) AS INTEGER) - 1
);

-- name: SetServiceFromTemplate :exec
-- Records the app template (ADR 0083) a service was created from.
UPDATE services SET from_template = ?, from_template_revision = ? WHERE id = +?;

-- name: CreatePITRRestore :one
-- A restore to a time (ADR 0093): from the base backup snapshot_id of
-- generation wal_generation, into restored_to.
INSERT INTO backups (service_id, operation, kind, trigger, status, repository, snapshot_id, restored_to, created_by, started_at,
    wal_generation, restore_to_time)
VALUES (?, 'restore', 'pitr', 'manual', 'running', ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: SetBackupBase :exec
-- Records what a finished base backup says about itself.
UPDATE backups SET wal_generation = ?, wal_start = ?, wal_segment_bytes = ?, consistent_at = ? WHERE id = +?;

-- name: ListPITRBases :many
-- A database's base backups that restore: succeeded, unpruned, in the
-- order they became consistent.
SELECT * FROM backups
WHERE service_id = +? AND kind = 'pitr' AND operation = 'backup' AND status = 'succeeded' AND pruned = 0
ORDER BY consistent_at;

-- name: DetachBaseBackupsWAL :exec
-- After a backup move: base backups were copied, but not the WAL they
-- replay from, so they no longer restore to a time; retention still ages
-- them out (ADR 0093).
UPDATE backups SET wal_start = '' WHERE kind = 'pitr' AND operation = 'backup' AND wal_start <> '';

-- name: CreateBackupCheck :one
INSERT INTO backup_checks (kind, server_id, service_id, repository, snapshot_id, trigger, status, started_at)
VALUES (?, ?, ?, ?, ?, ?, 'running', ?)
RETURNING *;

-- name: FinishBackupCheck :exec
UPDATE backup_checks SET status = ?, error = ?, detail = ?, finished_at = ? WHERE id = +?;

-- name: ListLatestRepositoryChecks :many
-- Each repository's latest check attempt per server (ADR 0096), for the
-- scheduler and backup settings.
SELECT * FROM backup_checks WHERE id IN (
    SELECT max(id) FROM backup_checks WHERE kind = 'repository' GROUP BY repository, server_id
)
ORDER BY repository, server_id;

-- name: TrimRepositoryChecks :exec
-- Keeps the newest ten checks of a repository on a server.
DELETE FROM backup_checks
WHERE backup_checks.kind = 'repository' AND backup_checks.repository = +sqlc.arg(repository) AND backup_checks.server_id = +sqlc.arg(server_id)
  AND backup_checks.id NOT IN (
    SELECT c.id FROM backup_checks c
    WHERE c.kind = 'repository' AND c.repository = +sqlc.arg(repository) AND c.server_id = +sqlc.arg(server_id)
    ORDER BY c.id DESC LIMIT 10
  );

-- name: FailRunningBackupChecks :execrows
-- At start-up: a check the last daemon left running never finished.
UPDATE backup_checks SET status = 'failed', error = ?, finished_at = ? WHERE status = 'running';

-- name: SetBackupVerify :exec
-- Turns the weekly checks and drills on or off. The settings row exists:
-- the caller opens the repository first (repoOf creates it).
UPDATE backup_settings SET verify = ?, updated_at = ?, updated_by = ? WHERE id = 1;

-- name: ListDrillCandidates :many
-- Every live template database (not archived or stopped, in a live
-- project, outside previews) with its newest snapshot a restore can use,
-- for the restore drills (ADR 0096). The snapshot is found by walking
-- backups_service down from the service's newest run.
SELECT services.id, environments.server_id, backups.snapshot_id, backups.repository, backups.size_bytes
FROM services
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
JOIN backups ON backups.id = (
    SELECT b.id FROM backups b
    WHERE b.service_id = services.id AND b.operation = 'backup' AND b.status = 'succeeded'
      AND b.pruned = 0 AND b.kind <> 'pitr' AND b.snapshot_id <> ''
    ORDER BY b.id DESC LIMIT 1
)
WHERE services.archived_at IS NULL AND services.stopped_at IS NULL AND projects.archived_at IS NULL
  AND services.template <> ''
  AND NOT EXISTS (SELECT 1 FROM previews WHERE previews.environment_id = services.environment_id)
ORDER BY services.id;

-- name: GetDrillSnapshot :one
-- A service's newest snapshot a restore can use: what its drill restores.
SELECT * FROM backups
WHERE service_id = +? AND operation = 'backup' AND status = 'succeeded'
  AND pruned = 0 AND kind <> 'pitr' AND snapshot_id <> ''
ORDER BY id DESC LIMIT 1;

-- name: ListLatestDrills :many
-- Each service's latest restore drill, for the scheduler.
SELECT * FROM backup_checks WHERE id IN (
    SELECT max(id) FROM backup_checks WHERE kind = 'drill' GROUP BY service_id
);

-- name: GetLatestDrill :one
-- A service's latest restore drill.
SELECT * FROM backup_checks WHERE kind = 'drill' AND service_id = +? ORDER BY id DESC LIMIT 1;

-- name: TrimDrillChecks :exec
-- Keeps the newest ten drills of a service.
DELETE FROM backup_checks
WHERE backup_checks.kind = 'drill' AND backup_checks.service_id = +sqlc.arg(service_id)
  AND backup_checks.id NOT IN (
    SELECT c.id FROM backup_checks c
    WHERE c.kind = 'drill' AND c.service_id = +sqlc.arg(service_id)
    ORDER BY c.id DESC LIMIT 10
  );

-- name: GetDrillRestore :one
-- The restore a drill ran from a service into another: how a service
-- named like a drill's is known to be one (created_by is the system's).
SELECT * FROM backups
WHERE service_id = +sqlc.arg(service_id) AND operation = 'restore' AND restored_to = +sqlc.arg(restored_to)
ORDER BY id DESC LIMIT 1;

-- name: GetRestoreInto :one
-- The newest restore of a backup or to a time that made the service (its
-- new service; an in-place restore's target is its own service and
-- doesn't count).
SELECT * FROM backups
WHERE restored_to = +? AND operation = 'restore' AND service_id != restored_to
ORDER BY id DESC LIMIT 1;
