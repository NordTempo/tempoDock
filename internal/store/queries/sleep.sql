-- ListSleepEnvironments is the sleep manager's load: every live
-- environment that may sleep on its own or is asleep, with its server.
-- name: ListSleepEnvironments :many
SELECT e.id, e.server_id, e.sleep_after_minutes, e.asleep_at
FROM environments e JOIN projects p ON p.id = e.project_id
WHERE (e.sleep_after_minutes > 0 OR e.asleep_at IS NOT NULL)
  AND e.archived_at IS NULL AND p.archived_at IS NULL;

-- name: SetEnvironmentAsleep :execrows
-- Marks an awake environment asleep: the target state, written before its
-- containers stop.
UPDATE environments SET asleep_at = ?, slept_by = ?, freed_bytes = ?
WHERE id = +? AND asleep_at IS NULL;

-- name: SetEnvironmentAwake :execrows
-- Clears an asleep environment's target once every service is back.
UPDATE environments SET asleep_at = NULL, woke_at = ?, woke_by = ?
WHERE id = +? AND asleep_at IS NOT NULL;

-- name: SetEnvironmentSleepAfter :one
UPDATE environments SET sleep_after_minutes = ? WHERE id = +? RETURNING *;

-- name: SetPreviewsSleepAfter :exec
-- Gives the open previews cloned from a base environment its setting.
UPDATE environments SET sleep_after_minutes = ?
WHERE archived_at IS NULL AND id IN (SELECT previews.environment_id FROM previews
                                     WHERE previews.base_environment_id = +sqlc.arg(base_environment_id) AND previews.closed_at IS NULL);

-- name: ListEnvironmentServiceReleases :many
-- The services of an environment with their active release's container,
-- for sleeping and waking it: live services that aren't stopped on
-- purpose (ADR 0050) and have a container.
SELECT services.id AS service_id, releases.id AS release_id, releases.container_id
FROM services JOIN releases ON releases.service_id = services.id AND releases.status = 'active'
WHERE services.environment_id = +? AND services.archived_at IS NULL AND services.stopped_at IS NULL
  AND releases.container_id != ''
ORDER BY services.id;

-- name: CountEnvironmentInFlight :one
-- Deploys in flight in an environment: it doesn't sleep under one.
SELECT count(*) FROM releases JOIN services ON services.id = releases.service_id
WHERE services.environment_id = +? AND releases.status IN ('queued', 'building', 'starting', 'health_checking');
