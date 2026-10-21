-- name: ListAuditEventsByTarget :many
-- One target's events in an environment, newest first. A service's events
-- (releases, settings, variables, volumes, domains) target
-- "project/environment/service".
SELECT * FROM audit_events
WHERE project_id = sqlc.arg(project_id)
  AND environment_id = sqlc.arg(environment_id)
  AND target = sqlc.arg(target)
  AND (action LIKE sqlc.narg(action_prefix) OR sqlc.narg(action_prefix) IS NULL)
  AND (id < sqlc.narg(before_id) OR sqlc.narg(before_id) IS NULL)
ORDER BY id DESC
LIMIT sqlc.arg(max_rows);
