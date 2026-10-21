-- name: NextReleaseNumber :one
SELECT CAST(coalesce(max(number), 0) + 1 AS INTEGER) AS next FROM releases WHERE service_id = ?;

-- name: CreateRelease :one
INSERT INTO releases (service_id, number, source, from_release, image_ref, image_id, config, host_port, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetRelease :one
SELECT * FROM releases WHERE id = ?;

-- name: GetReleaseByNumber :one
SELECT * FROM releases WHERE service_id = ? AND number = ?;

-- name: GetActiveRelease :one
SELECT * FROM releases WHERE service_id = ? AND status = 'active';

-- name: GetInFlightRelease :one
SELECT * FROM releases
WHERE service_id = ? AND status IN ('queued', 'building', 'starting', 'health_checking');

-- name: ListReleases :many
SELECT * FROM releases WHERE service_id = ? ORDER BY number DESC LIMIT ?;

-- name: ListActiveReleases :many
SELECT * FROM releases WHERE status = 'active';

-- name: ListInFlightReleases :many
SELECT * FROM releases WHERE status IN ('queued', 'building', 'starting', 'health_checking');

-- name: SetReleaseStatus :exec
UPDATE releases SET status = ? WHERE id = ?;

-- name: SetReleaseImage :exec
UPDATE releases SET image_ref = ?, image_id = ? WHERE id = ?;

-- name: SetReleaseContainer :exec
UPDATE releases SET container_id = ? WHERE id = ?;

-- name: FailRelease :exec
UPDATE releases SET status = 'failed', error = ?, error_code = ?, finished_at = ? WHERE id = ?;

-- name: SupersedeRelease :exec
UPDATE releases SET status = 'superseded' WHERE id = ? AND status = 'active';

-- name: ActivateRelease :exec
UPDATE releases SET status = 'active', finished_at = ? WHERE id = ?;

-- name: AddReleaseEvent :one
INSERT INTO release_events (release_id, message, created_at) VALUES (?, ?, ?) RETURNING *;

-- name: ListReleaseEvents :many
SELECT * FROM release_events WHERE release_id = ? ORDER BY id;

-- name: ListUsedHostPorts :many
-- Host ports held on one server. A release keeps its port until its
-- container is confirmed removed (ClearReleaseHostPort), so a container
-- that is still draining, or failed to stop, never shares it.
SELECT releases.host_port FROM releases
JOIN services ON services.id = releases.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.server_id = ? AND releases.host_port > 0;

-- name: ClearReleaseHostPort :exec
-- Frees a retired release's port once its containers are gone.
UPDATE releases SET host_port = 0 WHERE id = ? AND status IN ('failed', 'superseded');

-- name: ListRetiredPortHolders :many
-- Failed or superseded releases on one server that still hold a host port.
SELECT releases.id FROM releases
JOIN services ON services.id = releases.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.server_id = ? AND releases.host_port > 0
  AND releases.status IN ('failed', 'superseded');
