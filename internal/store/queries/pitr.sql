-- name: GetPITR :one
SELECT * FROM pitr WHERE service_id = +?;

-- name: UpsertPITR :exec
INSERT INTO pitr (service_id, enabled, days, generation, server_id, updated_by, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (service_id) DO UPDATE SET
    enabled = excluded.enabled, days = excluded.days, generation = excluded.generation,
    updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- name: SetPITRServer :exec
-- Where the database's archiver runs now (NULL: nowhere).
UPDATE pitr SET server_id = ? WHERE service_id = +?;

-- name: SetPITRGeneration :exec
UPDATE pitr SET generation = ? WHERE service_id = +?;

-- name: ListPITR :many
-- Every database with point-in-time restore set up, on or off: the
-- daemon's start reconciles their archivers. A few rows.
SELECT * FROM pitr ORDER BY service_id;

-- name: SetPITRWALBytes :exec
UPDATE pitr SET wal_bytes = ? WHERE service_id = +?;

-- name: ListEnvironmentDeploysSince :many
-- Releases of an environment's services that went live since a time,
-- newest first: the deploy marks on a database's point-in-time strip
-- (ADR 0093).
SELECT services.name AS service, releases.number, releases.finished_at
FROM releases JOIN services ON services.id = releases.service_id
WHERE services.environment_id = +sqlc.arg(environment_id) AND releases.status IN ('active', 'superseded')
  AND releases.finished_at >= +sqlc.arg(since)
ORDER BY releases.finished_at DESC LIMIT +sqlc.arg(lim);
