-- name: GetControlPlaneBackup :one
SELECT * FROM control_plane_backup WHERE id = 1;

-- name: StartControlPlaneBackup :one
-- Records a running control plane backup in the one row. A scheduled run
-- moves scheduled_at; a manual one keeps it.
INSERT INTO control_plane_backup (id, trigger, status, repository, created_by, started_at, scheduled_at)
VALUES (1, sqlc.arg(trigger), 'running', sqlc.arg(repository), sqlc.arg(created_by), sqlc.arg(started_at), sqlc.narg(scheduled_at))
ON CONFLICT (id) DO UPDATE SET
    trigger = excluded.trigger, status = 'running', repository = excluded.repository,
    snapshot_id = '', size_bytes = 0, error = '', created_by = excluded.created_by,
    started_at = excluded.started_at, finished_at = NULL,
    scheduled_at = coalesce(excluded.scheduled_at, control_plane_backup.scheduled_at)
RETURNING *;

-- name: FinishControlPlaneBackup :exec
UPDATE control_plane_backup SET status = ?, snapshot_id = ?, size_bytes = ?, error = ?, finished_at = ? WHERE id = 1;

-- name: FailRunningControlPlaneBackup :execrows
-- A run a restart cut short.
UPDATE control_plane_backup SET status = 'failed', error = ?, finished_at = ? WHERE id = 1 AND status = 'running';

-- name: ForgetRunningControlPlaneBackup :exec
-- A restored database holds the run that was taking the backup it came
-- from (ADR 0021); it never finished there, so it is dropped.
DELETE FROM control_plane_backup WHERE status = 'running';

-- name: DetachLocalServer :one
-- Turns the local server into a remote server waiting to join again,
-- when the control plane was restored on another server and the old one
-- becomes a node (ADR 0021). Without a key and revoked, `dock server add
-- <name>` re-joins it under its name with its environments.
UPDATE servers SET local = 0, key_fingerprint = NULL, next_key_fingerprint = NULL, revoked_at = ?
WHERE local = 1
RETURNING *;

-- name: MoveControlPlaneSnapshot :exec
-- Points the control plane's latest backup at its copy in another
-- repository (a backup move).
UPDATE control_plane_backup SET repository = ?, snapshot_id = ? WHERE id = 1 AND repository = +? AND snapshot_id = +?;

-- name: RelabelControlPlaneRepository :exec
UPDATE control_plane_backup SET repository = ? WHERE id = 1 AND repository = +?;
