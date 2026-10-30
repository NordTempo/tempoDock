-- name: ListStorageConnections :many
SELECT * FROM storage_connections ORDER BY name;

-- name: GetStorageConnection :one
SELECT * FROM storage_connections WHERE name = +?;

-- name: CreateStorageConnection :one
INSERT INTO storage_connections (name, endpoint, region, bucket, path_style, credentials, created_at, updated_at, updated_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateStorageConnection :one
UPDATE storage_connections
SET name = ?, endpoint = ?, region = ?, bucket = ?, path_style = ?, credentials = ?, updated_at = ?, updated_by = ?
WHERE id = +?
RETURNING *;

-- name: DeleteStorageConnection :exec
DELETE FROM storage_connections WHERE id = +?;

-- name: GetRegistryStorageID :one
-- The storage connection the registry keeps its images in; no row, or
-- NULL, while they are on the control plane's disk.
SELECT registry_storage_id FROM platform_settings WHERE id = 1;

-- name: SetRegistryStorage :exec
INSERT INTO platform_settings (id, registry_storage_id, updated_by, updated_at) VALUES (1, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    registry_storage_id = excluded.registry_storage_id, updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- name: GetStorageConnectionByID :one
SELECT * FROM storage_connections WHERE id = +?;
