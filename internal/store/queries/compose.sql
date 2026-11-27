-- name: GetComposeSource :one
SELECT * FROM compose_sources WHERE environment_id = +?;

-- name: GetComposeSourceByID :one
SELECT * FROM compose_sources WHERE id = +?;

-- name: UpsertComposeSource :one
-- Sets what an environment follows. A change of repository, branch or
-- path forgets what the last sync read and any queued push (it was the
-- old target's); its outcome stays until the next sync replaces it.
INSERT INTO compose_sources (environment_id, installation_id, repo_id, repo, branch, path, prune, auto_apply, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (environment_id) DO UPDATE SET
    installation_id = excluded.installation_id, repo_id = excluded.repo_id, repo = excluded.repo,
    reads = CASE WHEN compose_sources.repo_id = excluded.repo_id AND compose_sources.branch = excluded.branch
                  AND compose_sources.path = excluded.path THEN compose_sources.reads ELSE '' END,
    pending_sha = CASE WHEN compose_sources.repo_id = excluded.repo_id AND compose_sources.branch = excluded.branch
                  AND compose_sources.path = excluded.path THEN compose_sources.pending_sha ELSE '' END,
    pending_by = CASE WHEN compose_sources.repo_id = excluded.repo_id AND compose_sources.branch = excluded.branch
                  AND compose_sources.path = excluded.path THEN compose_sources.pending_by ELSE '' END,
    branch = excluded.branch, path = excluded.path, prune = excluded.prune, auto_apply = excluded.auto_apply
RETURNING *;

-- name: DeleteComposeSource :execrows
-- Deletes the environment's source, if it is still the one read (the
-- caller held its sync).
DELETE FROM compose_sources WHERE id = +sqlc.arg(id) AND environment_id = +sqlc.arg(environment_id);

-- name: ListRepoComposeSources :many
-- The sources one App push delivery may sync.
SELECT * FROM compose_sources WHERE repo_id = +? AND installation_id = +?;

-- name: ListPendingComposeSources :many
-- Sources with a push waiting, resumed at start-up.
SELECT id FROM compose_sources WHERE pending_sha > '';

-- name: SetComposePending :execrows
-- Queues a commit to sync, replacing any older one that hasn't started,
-- if the source still follows the branch and path the push was matched
-- against (it may have been changed meanwhile).
UPDATE compose_sources SET pending_sha = ?, pending_by = ?
WHERE id = +sqlc.arg(id) AND repo_id = +sqlc.arg(repo_id) AND branch = +sqlc.arg(branch) AND path = +sqlc.arg(path);

-- name: FinishComposeSync :one
-- Records a sync's outcome and clears the queued commit if it is still
-- the one synced (a newer push may have replaced it meanwhile, and runs
-- next).
UPDATE compose_sources
SET last_sha = sqlc.arg(last_sha), last_message = sqlc.arg(last_message), last_author = sqlc.arg(last_author),
    last_at = sqlc.arg(last_at), outcome = sqlc.arg(outcome), summary = sqlc.arg(summary), reads = sqlc.arg(reads),
    pending_sha = CASE WHEN pending_sha = sqlc.arg(synced) THEN '' ELSE pending_sha END
WHERE id = +sqlc.arg(id)
RETURNING *;
