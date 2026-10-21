-- name: AddAuditEvent :one
INSERT INTO audit_events (actor, actor_kind, user_id, token_id, action, project_id, environment_id, target, summary, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAuditEvents :many
-- Newest first, with project and environment names. Nil filters match
-- everything; only events with IDs below before_id are returned (pass
-- math.MaxInt64 for the newest). An action prefix is the range
-- [action_from, action_to) (see core.prefixRange): LIKE would make SQLite
-- re-prepare the statement on every call. Use ListProjectAuditEvents to filter by
-- project: an optional project filter here would keep SQLite from using
-- the audit_events_project index.
SELECT sqlc.embed(audit_events),
       coalesce(projects.name, '') AS project_name, coalesce(environments.name, '') AS environment_name
FROM audit_events
LEFT JOIN projects ON projects.id = audit_events.project_id
LEFT JOIN environments ON environments.id = audit_events.environment_id
WHERE audit_events.id < +sqlc.arg(before_id)
  AND (audit_events.environment_id = +sqlc.narg(environment_id) OR sqlc.narg(environment_id) IS NULL)
  AND ((audit_events.action >= +sqlc.narg(action_from) AND audit_events.action < +sqlc.narg(action_to)) OR sqlc.narg(action_from) IS NULL)
ORDER BY audit_events.id DESC
LIMIT +sqlc.arg(max_rows);

-- name: ListProjectAuditEvents :many
-- ListAuditEvents for one project, read through audit_events_project.
SELECT sqlc.embed(audit_events),
       coalesce(projects.name, '') AS project_name, coalesce(environments.name, '') AS environment_name
FROM audit_events
LEFT JOIN projects ON projects.id = audit_events.project_id
LEFT JOIN environments ON environments.id = audit_events.environment_id
WHERE audit_events.project_id = +sqlc.arg(project_id)
  AND audit_events.id < +sqlc.arg(before_id)
  AND (audit_events.environment_id = +sqlc.narg(environment_id) OR sqlc.narg(environment_id) IS NULL)
  AND ((audit_events.action >= +sqlc.narg(action_from) AND audit_events.action < +sqlc.narg(action_to)) OR sqlc.narg(action_from) IS NULL)
ORDER BY audit_events.id DESC
LIMIT +sqlc.arg(max_rows);
