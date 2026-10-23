-- name: GetServiceAccess :one
SELECT * FROM service_access WHERE service_id = +?;

-- name: SetServiceAccess :exec
INSERT INTO service_access (service_id, mode, username, password_hash, key, updated_by, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (service_id) DO UPDATE SET
    mode = excluded.mode, username = excluded.username, password_hash = excluded.password_hash,
    key = excluded.key, updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- name: DeleteServiceAccess :exec
DELETE FROM service_access WHERE service_id = +?;
