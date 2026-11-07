-- name: CreateEnvironmentMove :one
INSERT INTO environment_moves (environment_id, from_server_id, to_server_id, max_hold_s, drain_id, created_by, started_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: AddEnvironmentMoveService :exec
INSERT INTO environment_move_services (move_id, service_id, release_id, stateful, host_port) VALUES (?, ?, ?, ?, ?);

-- name: GetEnvironmentMove :one
SELECT * FROM environment_moves WHERE id = +?;

-- name: GetLatestEnvironmentMove :one
SELECT * FROM environment_moves WHERE environment_id = +? ORDER BY id DESC LIMIT 1;

-- name: GetOpenEnvironmentMove :one
-- The move of an environment that hasn't finished, if any.
SELECT * FROM environment_moves WHERE environment_id = +? AND status IN ('running', 'rolling_back');

-- name: ListOpenEnvironmentMoves :many
SELECT * FROM environment_moves WHERE status IN ('running', 'rolling_back') ORDER BY id;

-- name: ListEnvironmentMoveServices :many
SELECT * FROM environment_move_services WHERE move_id = +? ORDER BY stateful DESC, service_id;

-- name: SetEnvironmentMoveStep :exec
UPDATE environment_moves SET step = ? WHERE id = +?;

-- name: SetEnvironmentMoveStatus :exec
UPDATE environment_moves SET status = ?, error = ? WHERE id = +?;

-- name: SetEnvironmentMoveDowntime :exec
UPDATE environment_moves SET downtime_from = coalesce(downtime_from, ?), downtime_to = ? WHERE id = +?;

-- name: FinishEnvironmentMove :exec
UPDATE environment_moves SET status = ?, step = ?, error = ?, notes = ?, finished_at = ? WHERE id = +?;

-- name: SetEnvironmentMoveService :exec
UPDATE environment_move_services SET status = ?, container_id = ?, copied_bytes = ?, error = ? WHERE move_id = +? AND service_id = +?;

-- name: SetEnvironmentMovePass :exec
-- Records a service's live pass: how many so far, how long the last one
-- took and what it copied in all.
UPDATE environment_move_services SET status = 'syncing', passes = ?, last_pass_ms = ?, copied_bytes = ?
WHERE move_id = +? AND service_id = +?;

-- name: SetEnvironmentServer :exec
UPDATE environments SET server_id = ? WHERE id = +?;

-- name: MoveReleaseServing :exec
-- Points an active release at its container and host port on the server
-- its environment moved to.
UPDATE releases SET host_port = ?, container_id = ? WHERE id = +?;

-- name: RetargetCloudflareRecords :many
-- Points Dock's Cloudflare records of an environment's domains at the
-- server it moved to, to be routed again; returns the hostnames.
UPDATE cloudflare_records SET server_id = +sqlc.arg(server_id), status = 'pending', attempts = 0, retry_at = NULL,
    gen = gen + 1, updated_at = sqlc.arg(updated_at)
WHERE want = 'present' AND domain_id IN (
    SELECT domains.id FROM domains JOIN services ON services.id = domains.service_id
    WHERE services.environment_id = +sqlc.arg(environment_id)
)
RETURNING hostname;

-- name: CountEnvironmentCloudflareRecords :one
-- How many of an environment's domains Dock routes through Cloudflare.
SELECT count(*) FROM cloudflare_records
JOIN domains ON domains.id = cloudflare_records.domain_id
JOIN services ON services.id = domains.service_id
WHERE services.environment_id = +? AND cloudflare_records.want = 'present';

-- name: CountEnvironmentSnapshotsOn :one
-- How many volume snapshots of an environment's services are on a server.
SELECT count(*) FROM volume_snapshots
JOIN services ON services.id = volume_snapshots.service_id
WHERE services.environment_id = +sqlc.arg(environment_id) AND volume_snapshots.server_id = +sqlc.arg(server_id);

-- name: ListEnvironmentSnapshotsOn :many
-- An environment's finished volume snapshots on a server, which a move
-- copies to its target.
SELECT volume_snapshots.* FROM volume_snapshots
JOIN services ON services.id = volume_snapshots.service_id
WHERE services.environment_id = +sqlc.arg(environment_id) AND volume_snapshots.server_id = +sqlc.arg(server_id)
  AND volume_snapshots.status = 'ready'
ORDER BY volume_snapshots.id;

-- name: MoveEnvironmentSnapshots :exec
-- Points an environment's snapshots on one server at the server the move
-- copied them to.
UPDATE volume_snapshots SET server_id = +sqlc.arg(to_server_id)
WHERE server_id = +sqlc.arg(from_server_id) AND status = 'ready' AND service_id IN (
    SELECT id FROM services WHERE environment_id = +sqlc.arg(environment_id)
);
