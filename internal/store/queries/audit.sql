-- name: AddAuditEvent :one
INSERT INTO audit_events (actor, actor_kind, user_id, token_id, action, project_id, environment_id, target, summary, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAuditEvents :many
-- Newest first. Nil filters match everything; before_id pages backwards.
SELECT * FROM audit_events
WHERE (project_id = sqlc.narg(project_id) OR sqlc.narg(project_id) IS NULL)
  AND (environment_id = sqlc.narg(environment_id) OR sqlc.narg(environment_id) IS NULL)
  AND (action LIKE sqlc.narg(action_prefix) OR sqlc.narg(action_prefix) IS NULL)
  AND (id < sqlc.narg(before_id) OR sqlc.narg(before_id) IS NULL)
ORDER BY id DESC
LIMIT sqlc.arg(max_rows);
