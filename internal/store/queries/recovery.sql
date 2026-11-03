-- name: AddEnsureRunning :exec
INSERT INTO ensure_running (service_id) VALUES (?) ON CONFLICT DO NOTHING;

-- name: ListEnsureRunning :many
-- The services on a server whose active release must be started again.
SELECT r.service_id FROM ensure_running r
JOIN services s ON s.id = r.service_id
JOIN environments e ON e.id = s.environment_id
WHERE e.server_id = +sqlc.arg(server_id);

-- name: DeleteEnsureRunning :exec
DELETE FROM ensure_running WHERE service_id = +?;
