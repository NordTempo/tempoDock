-- What deleting an archived project (ADR 0077) removes outside the
-- database, read before its rows go.

-- name: ListAllProjectEnvironments :many
-- A project's environments, archived ones (closed previews) included:
-- their networks and volumes may still be on their servers.
SELECT * FROM environments WHERE project_id = +? ORDER BY id;

-- name: ListProjectSnapshots :many
-- Every volume snapshot of a project's services, whose files are on
-- server_id.
SELECT volume_snapshots.* FROM volume_snapshots
JOIN services ON services.id = volume_snapshots.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.project_id = +?
ORDER BY volume_snapshots.id;

-- name: ListProjectKeptBackups :many
-- How many restic snapshots each of a project's services still has, by
-- repository: the backups deleting it leaves in place.
SELECT backups.service_id, backups.repository, CAST(count(*) AS INTEGER) AS snapshots FROM backups
JOIN services ON services.id = backups.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.project_id = +? AND backups.operation = 'backup' AND backups.status = 'succeeded'
  AND NOT backups.pruned AND backups.snapshot_id != ''
GROUP BY backups.service_id, backups.repository
ORDER BY backups.repository, backups.service_id;
