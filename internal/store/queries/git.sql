-- name: GetGitConnectionByService :one
SELECT * FROM git_connections WHERE service_id = +?;

-- name: GetGitConnectionByHook :one
SELECT * FROM git_connections WHERE hook_id = +?;

-- name: CreateGitConnection :one
INSERT INTO git_connections (service_id, hook_id, repo, branch, dir, secret, token, post_status, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateGitConnection :one
-- Changes what a connection deploys. hook_id and secret change only when
-- the secret is rotated (the caller passes the old ones otherwise).
UPDATE git_connections
SET repo = ?, branch = ?, dir = ?, token = ?, post_status = ?, hook_id = ?, secret = ?
WHERE id = +?
RETURNING *;

-- name: DeleteGitConnection :exec
DELETE FROM git_connections WHERE service_id = +?;

-- name: SetGitPending :exec
-- Queues a push, replacing any older one that hasn't started.
UPDATE git_connections
SET pending_sha = ?, pending_message = ?, pending_author = ?, pending_by = ?, pending_force = ?
WHERE id = +?;

-- name: ClearGitPending :exec
-- Clears the queued push if it is still sha (a newer one may have
-- replaced it meanwhile).
UPDATE git_connections
SET pending_sha = '', pending_message = '', pending_author = '', pending_by = '', pending_force = 0
WHERE id = +sqlc.arg(id) AND pending_sha = CAST(sqlc.arg(pending_sha) AS TEXT);

-- name: SetGitLastEvent :exec
UPDATE git_connections SET last_event = ?, last_event_at = ? WHERE id = +?;

-- name: ListGitPendingServices :many
SELECT service_id FROM git_connections WHERE pending_sha > '';

-- name: AddGitDelivery :exec
INSERT INTO git_deliveries (delivery_id, created_at) VALUES (?, ?);

-- name: PruneGitDeliveries :exec
DELETE FROM git_deliveries WHERE created_at < ?;

-- name: SetReleaseCommit :exec
UPDATE releases SET commit_sha = ?, commit_message = ?, commit_author = ? WHERE id = +?;
