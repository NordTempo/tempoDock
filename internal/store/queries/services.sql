-- name: CreateService :one
INSERT INTO services (environment_id, name, image, port, health_check_path, health_check_timeout_seconds,
                      start_command, dockerfile_path, build_command, memory_mb, cpus,
                      health_check_interval_seconds, health_check_probe_timeout_seconds,
                      health_check_retries, health_check_start_period_seconds,
                      pre_deploy_command, pre_deploy_timeout_seconds, pre_deploy_snapshot)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetService :one
SELECT * FROM services WHERE id = +?;

-- name: GetServiceByName :one
SELECT * FROM services WHERE environment_id = +? AND name = +?;

-- name: ListServices :many
SELECT * FROM services WHERE environment_id = +? ORDER BY name;

-- name: UpdateService :one
UPDATE services SET
    image = ?, port = ?, health_check_path = ?, health_check_timeout_seconds = ?,
    start_command = ?, dockerfile_path = ?, build_command = ?, memory_mb = ?, cpus = ?,
    health_check_interval_seconds = ?, health_check_probe_timeout_seconds = ?,
    health_check_retries = ?, health_check_start_period_seconds = ?,
    pre_deploy_command = ?, pre_deploy_timeout_seconds = ?, pre_deploy_snapshot = ?
WHERE id = +?
RETURNING *;

-- name: ArchiveService :exec
UPDATE services SET archived_at = ? WHERE id = +?;

-- name: StopService :execrows
-- Marks a service stopped (ADR 0050). 0 rows: it already was.
UPDATE services SET stopped_at = ?, stopped_by = ? WHERE id = +? AND stopped_at IS NULL;

-- name: ClearServiceStopped :exec
-- A release of a stopped service went live: it runs again (ADR 0050).
UPDATE services SET stopped_at = NULL, stopped_by = '' WHERE id = +?;

-- name: SetPendingSettings :exec
UPDATE services SET pending_settings = ? WHERE id = +?;

-- name: ClearEnvironmentPendingSettings :exec
-- Drops every staged settings change in an environment (apply and discard).
UPDATE services SET pending_settings = NULL WHERE environment_id = +? AND pending_settings IS NOT NULL;

-- name: CreateVolume :one
INSERT INTO volumes (service_id, name, docker_name, mount_path, pending) VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: ListVolumes :many
SELECT * FROM volumes WHERE service_id = +? ORDER BY name;

-- name: ApplyVolume :exec
UPDATE volumes SET pending = 0 WHERE id = +?;

-- name: DeleteVolume :exec
-- Only for discarding staged (never created) volumes.
DELETE FROM volumes WHERE id = +? AND pending = 1;

-- name: ListVariables :many
-- All variables of an environment: shared ones (service_id NULL) and every
-- service's own, live and staged.
SELECT * FROM variables WHERE environment_id = +? ORDER BY coalesce(service_id, 0), key;

-- name: GetVariable :one
SELECT * FROM variables
WHERE environment_id = +sqlc.arg(environment_id)
  AND coalesce(service_id, 0) = coalesce(sqlc.narg(service_id), 0)
  AND key = +sqlc.arg(key);

-- name: InsertVariable :exec
INSERT INTO variables (environment_id, service_id, key, pending, pending_value, updated_at)
VALUES (?, ?, ?, 'set', ?, ?);

-- name: SetVariablePending :exec
UPDATE variables SET pending = ?, pending_value = ?, updated_at = ? WHERE id = +?;

-- name: ApplyVariable :exec
UPDATE variables SET value = pending_value, pending = '', pending_value = NULL WHERE id = +?;

-- name: DeleteVariable :exec
DELETE FROM variables WHERE id = +?;

-- name: ListProjectServices :many
-- Every service in a project, by environment, then name.
SELECT services.* FROM services
JOIN environments ON environments.id = services.environment_id
WHERE environments.project_id = +?
ORDER BY services.environment_id, services.name;

-- name: ListProjectVolumes :many
-- Every volume of every service in a project, including staged ones.
SELECT volumes.* FROM volumes
JOIN services ON services.id = volumes.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.project_id = +?
ORDER BY volumes.service_id, volumes.name;

-- name: ListEnvironmentVolumes :many
-- Every volume of every service in an environment, including staged ones.
SELECT volumes.* FROM volumes
JOIN services ON services.id = volumes.service_id
WHERE services.environment_id = +?
ORDER BY volumes.service_id, volumes.name;

-- name: RestoreVariable :exec
-- Puts back a variable's live value and staged change (restaging the changes
-- of a failed release).
UPDATE variables SET value = ?, pending = ?, pending_value = ?, updated_at = ? WHERE id = +?;

-- name: InsertRestoredVariable :exec
-- Re-creates a variable an applied unset deleted, with the unset staged.
INSERT INTO variables (environment_id, service_id, key, value, pending, updated_at)
VALUES (?, ?, ?, ?, 'unset', ?);

-- name: UnapplyVolume :exec
-- Stages an applied volume again (its release failed). Never deletes it.
UPDATE volumes SET pending = 1 WHERE id = +? AND pending = 0;

-- name: SetEnvironmentApplyError :exec
UPDATE environments SET apply_error = ? WHERE id = +?;

-- name: ListServerServices :many
-- The services of the environments on a server, with their active
-- release: the services table of a server page.
SELECT services.id, services.name, services.memory_mb, services.cpus,
  projects.name AS project_name, environments.name AS environment_name,
  releases.number AS active_release
FROM environments
JOIN projects ON projects.id = environments.project_id
JOIN services ON services.environment_id = environments.id
LEFT JOIN releases ON releases.service_id = services.id AND releases.status = 'active'
WHERE environments.server_id = +? AND projects.archived_at IS NULL AND services.archived_at IS NULL
ORDER BY projects.name, environments.name, services.name;

-- name: ListServiceIDs :many
-- Every service's ID, archived ones included: the log archives the
-- retention pass keeps (ADR 0046).
SELECT id FROM services ORDER BY id;
