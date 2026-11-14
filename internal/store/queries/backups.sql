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
-- Records the app template (ADR 0081) a service was created from.
UPDATE services SET from_template = ?, from_template_revision = ? WHERE id = +?;
