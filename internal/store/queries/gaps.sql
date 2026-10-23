-- name: ListAuditEventsByTarget :many
-- One target's events in an environment, newest first. A service's events
-- (releases, settings, variables, volumes, domains) target
-- "project/environment/service". Match the environment/service suffix so
-- historical targets remain readable after a project rename.
SELECT * FROM audit_events
WHERE project_id = +sqlc.arg(project_id)
  AND environment_id = +sqlc.arg(environment_id)
  AND substr(target, instr(target, '/') + 1) = +sqlc.arg(target)
  AND ((action >= +sqlc.narg(action_from) AND action < +sqlc.narg(action_to)) OR sqlc.narg(action_from) IS NULL)
  AND (id < +sqlc.narg(before_id) OR sqlc.narg(before_id) IS NULL)
ORDER BY id DESC
LIMIT +sqlc.arg(max_rows);
