-- name: GetPublicHost :one
-- Dock's public address; no row until one was first set.
SELECT public_host FROM platform_settings WHERE id = 1;

-- name: SetPublicHost :exec
INSERT INTO platform_settings (id, public_host, updated_by, updated_at) VALUES (1, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    public_host = excluded.public_host, updated_by = excluded.updated_by, updated_at = excluded.updated_at;
