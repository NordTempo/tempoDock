-- The audit log. Lists page back with before_id (pass math.MaxInt64 for
-- the newest); each filter combination has its own query and index.

-- name: AddAuditEvent :exec
INSERT INTO audit_events (created_at, actor, actor_kind, user_id, token_id, action, connection_id, target, summary, detail_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: AddAuditDetail :one
-- The caller cuts sql at apitypes.AuditSQLMaxBytes.
INSERT INTO audit_details (sql, rows, duration_ms, outcome) VALUES (?, ?, ?, ?)
RETURNING id;

-- name: GetAuditDetail :one
-- An audit event's detail by its detail_id.
SELECT * FROM audit_details WHERE id = +?;

-- name: ListAuditEvents :many
SELECT * FROM audit_events
WHERE id < sqlc.arg(before_id)
ORDER BY id DESC LIMIT +sqlc.arg(max_rows);

-- name: ListAuditEventsByConnection :many
SELECT * FROM audit_events
WHERE connection_id = +sqlc.arg(connection_id) AND id < sqlc.arg(before_id)
ORDER BY id DESC LIMIT +sqlc.arg(max_rows);

-- name: ListAuditEventsByAction :many
SELECT * FROM audit_events
WHERE action = +sqlc.arg(action) AND id < sqlc.arg(before_id)
ORDER BY id DESC LIMIT +sqlc.arg(max_rows);

-- name: ListAuditEventsByConnectionAction :many
SELECT * FROM audit_events
WHERE connection_id = +sqlc.arg(connection_id) AND action = +sqlc.arg(action) AND id < sqlc.arg(before_id)
ORDER BY id DESC LIMIT +sqlc.arg(max_rows);

-- name: ListUserQueryHistory :many
-- One user's data.query events on one connection, newest first.
SELECT audit_events.id AS audit_id, audit_events.created_at, sqlc.embed(audit_details)
FROM audit_events
JOIN audit_details ON audit_details.id = audit_events.detail_id
WHERE audit_events.action = 'data.query'
  AND audit_events.connection_id = +sqlc.arg(connection_id)
  AND audit_events.user_id = +sqlc.arg(user_id)
  AND audit_events.id < sqlc.arg(before_id)
ORDER BY audit_events.id DESC
LIMIT +sqlc.arg(max_rows);

-- name: ListTokenQueryHistory :many
-- One token's data.query events on one connection, newest first.
SELECT audit_events.id AS audit_id, audit_events.created_at, sqlc.embed(audit_details)
FROM audit_events
JOIN audit_details ON audit_details.id = audit_events.detail_id
WHERE audit_events.action = 'data.query'
  AND audit_events.connection_id = +sqlc.arg(connection_id)
  AND audit_events.token_id = +sqlc.arg(token_id)
  AND audit_events.id < sqlc.arg(before_id)
ORDER BY audit_events.id DESC
LIMIT +sqlc.arg(max_rows);

-- name: PruneAuditDetails :exec
-- The retention pass, step one: deletes the details of up to 1000 events
-- older than before, which deletes those events (ON DELETE CASCADE).
DELETE FROM audit_details WHERE id IN (
    SELECT detail_id FROM audit_events
    WHERE created_at < sqlc.arg(before) AND detail_id IS NOT NULL
    ORDER BY created_at LIMIT 1000
);

-- name: PruneAuditEvents :exec
-- The retention pass, step two: up to 1000 more events older than before.
DELETE FROM audit_events WHERE id IN (
    SELECT old.id FROM audit_events AS old WHERE old.created_at < sqlc.arg(before)
    ORDER BY old.created_at LIMIT 1000
);
