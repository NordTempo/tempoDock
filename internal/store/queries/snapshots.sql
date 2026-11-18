-- name: CreateSnapshot :one
INSERT INTO volume_snapshots (service_id, server_id, trigger, status, volumes, note, created_by, started_at)
VALUES (?, ?, ?, 'running', ?, ?, ?, ?)
RETURNING *;

-- name: FinishSnapshot :exec
UPDATE volume_snapshots SET status = ?, size_bytes = ?, paused_ms = ?, error = ?, log = ?, finished_at = ? WHERE id = +?;

-- name: GetSnapshot :one
SELECT * FROM volume_snapshots WHERE id = +?;

-- name: ListServiceSnapshots :many
-- A service's snapshots, newest first.
SELECT * FROM volume_snapshots WHERE service_id = +? ORDER BY id DESC LIMIT +?;

-- name: ListExpiredSnapshots :many
-- Finished snapshots taken before a time, oldest first, for retention.
-- Ordered by started_at so it seeks volume_snapshots_started instead of
-- scanning the table in ID order.
SELECT * FROM volume_snapshots WHERE started_at < +? AND status != 'running' ORDER BY started_at, id LIMIT +?;

-- name: DeleteSnapshot :exec
DELETE FROM volume_snapshots WHERE id = +?;

-- name: FailRunningSnapshots :execrows
UPDATE volume_snapshots SET status = 'failed', error = ?, finished_at = ? WHERE status = 'running';

-- name: CreateSnapshotRestore :one
INSERT INTO volume_snapshot_restores (snapshot_id, service_id, to_service_id, in_place, safety_id, status, created_by, started_at)
VALUES (?, ?, ?, ?, ?, 'running', ?, ?)
RETURNING *;

-- name: FinishSnapshotRestore :exec
UPDATE volume_snapshot_restores SET status = ?, error = ?, finished_at = ? WHERE id = +?;

-- name: ListServiceSnapshotRestores :many
-- A service's restores (into it, or from its snapshots), newest first.
SELECT * FROM volume_snapshot_restores WHERE service_id = +? ORDER BY id DESC LIMIT +?;

-- name: ListRunningSnapshotRestores :many
SELECT * FROM volume_snapshot_restores WHERE status = 'running';

-- name: GetSnapshotRestoreInto :one
-- The newest snapshot restore that made the service (into a new service,
-- not in place).
SELECT * FROM volume_snapshot_restores
WHERE to_service_id = +? AND in_place = 0
ORDER BY id DESC LIMIT 1;
