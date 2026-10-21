-- name: CreateService :one
INSERT INTO services (environment_id, name, image, port, health_check_path, health_check_timeout_seconds,
                      start_command, dockerfile_path, build_command, memory_mb, cpus)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetService :one
SELECT * FROM services WHERE id = ?;

-- name: GetServiceByName :one
SELECT * FROM services WHERE environment_id = ? AND name = ?;

-- name: ListServices :many
SELECT * FROM services WHERE environment_id = ? ORDER BY name;

-- name: UpdateService :one
UPDATE services SET
    image = ?, port = ?, health_check_path = ?, health_check_timeout_seconds = ?,
    start_command = ?, dockerfile_path = ?, build_command = ?, memory_mb = ?, cpus = ?
WHERE id = ?
RETURNING *;

-- name: ArchiveService :exec
UPDATE services SET archived_at = ? WHERE id = ?;

-- name: SetPendingSettings :exec
UPDATE services SET pending_settings = ? WHERE id = ?;

-- name: CreateVolume :one
INSERT INTO volumes (service_id, name, docker_name, mount_path, pending) VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: ListVolumes :many
SELECT * FROM volumes WHERE service_id = ? ORDER BY name;

-- name: ApplyVolume :exec
UPDATE volumes SET pending = 0 WHERE id = ?;

-- name: DeleteVolume :exec
-- Only for discarding staged (never created) volumes.
DELETE FROM volumes WHERE id = ? AND pending = 1;

-- name: ListVariables :many
-- All variables of an environment: shared ones (service_id NULL) and every
-- service's own, live and staged.
SELECT * FROM variables WHERE environment_id = ? ORDER BY coalesce(service_id, 0), key;

-- name: GetVariable :one
SELECT * FROM variables
WHERE environment_id = sqlc.arg(environment_id)
  AND coalesce(service_id, 0) = coalesce(sqlc.narg(service_id), 0)
  AND key = sqlc.arg(key);

-- name: InsertVariable :exec
INSERT INTO variables (environment_id, service_id, key, pending, pending_value, updated_at)
VALUES (?, ?, ?, 'set', ?, ?);

-- name: SetVariablePending :exec
UPDATE variables SET pending = ?, pending_value = ?, updated_at = ? WHERE id = ?;

-- name: ApplyVariable :exec
UPDATE variables SET value = pending_value, pending = '', pending_value = NULL WHERE id = ?;

-- name: DeleteVariable :exec
DELETE FROM variables WHERE id = ?;
