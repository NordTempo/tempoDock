-- name: NextReleaseNumber :one
SELECT CAST(coalesce(max(number), 0) + 1 AS INTEGER) AS next FROM releases WHERE service_id = +?;

-- name: CreateRelease :one
INSERT INTO releases (service_id, number, source, from_release, image_ref, image_id, image_digest, built_on_server_id, config, host_port, created_by, carried)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetRelease :one
SELECT * FROM releases WHERE id = +?;

-- name: GetReleaseByNumber :one
SELECT * FROM releases WHERE service_id = +? AND number = +?;

-- name: GetActiveRelease :one
SELECT * FROM releases WHERE service_id = +? AND status = 'active';

-- name: GetInFlightRelease :one
SELECT * FROM releases
WHERE service_id = +? AND status IN ('queued', 'building', 'starting', 'health_checking');

-- name: ListReleases :many
SELECT * FROM releases WHERE service_id = +? ORDER BY number DESC LIMIT +?;

-- name: ListActiveReleases :many
SELECT * FROM releases WHERE status = 'active';

-- name: ListInFlightReleases :many
SELECT * FROM releases WHERE status IN ('queued', 'building', 'starting', 'health_checking');

-- name: SetReleaseStatus :exec
UPDATE releases SET status = ? WHERE id = +?;

-- name: SetReleaseImage :exec
UPDATE releases SET image_ref = ?, image_id = ? WHERE id = +?;

-- name: SetReleaseContainer :exec
UPDATE releases SET container_id = ? WHERE id = +?;

-- name: FailRelease :exec
UPDATE releases SET status = 'failed', error = ?, error_code = ?, finished_at = ? WHERE id = +?;

-- name: SupersedeRelease :exec
UPDATE releases SET status = 'superseded' WHERE id = +? AND status = 'active';

-- name: ActivateRelease :exec
UPDATE releases SET status = 'active', finished_at = ? WHERE id = +?;

-- name: AddReleaseEvent :one
INSERT INTO release_events (release_id, message, created_at) VALUES (?, ?, ?) RETURNING *;

-- name: ListReleaseEvents :many
SELECT * FROM release_events WHERE release_id = +? ORDER BY id;

-- name: ListUsedHostPorts :many
-- Host ports held on one server. A release keeps its port until its
-- container is confirmed removed (ClearReleaseHostPort), so a container
-- that is still draining, or failed to stop, never shares it. An open
-- environment move holds the ports it reserved for the services it
-- starts there (ADR 0053).
SELECT releases.host_port FROM releases
JOIN services ON services.id = releases.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.server_id = +sqlc.arg(server_id) AND releases.host_port > 0
UNION ALL
SELECT environment_move_services.host_port FROM environment_move_services
JOIN environment_moves ON environment_moves.id = environment_move_services.move_id
WHERE environment_moves.to_server_id = +sqlc.arg(server_id)
  AND environment_moves.status IN ('running', 'rolling_back') AND environment_move_services.host_port > 0;

-- name: ClearReleaseHostPort :exec
-- Frees a retired release's port once its containers are gone.
UPDATE releases SET host_port = 0 WHERE id = +? AND status IN ('failed', 'superseded');

-- name: ListRetiredPortHolders :many
-- Failed or superseded releases on one server that still hold a host port.
SELECT releases.id FROM releases
JOIN services ON services.id = releases.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.server_id = +? AND releases.host_port > 0
  AND releases.status IN ('failed', 'superseded');

-- name: ListProjectReleaseHeads :many
-- For every service in a project: its active, in-flight and latest
-- releases. A release in several of these roles appears once per role.
WITH svc AS (
    SELECT services.id FROM services
    JOIN environments ON environments.id = services.environment_id
    WHERE environments.project_id = +?
)
SELECT releases.* FROM releases
WHERE releases.status = 'active' AND releases.service_id IN (SELECT id FROM svc)
UNION ALL
SELECT releases.* FROM releases
WHERE releases.status IN ('queued', 'building', 'starting', 'health_checking')
  AND releases.service_id IN (SELECT id FROM svc)
UNION ALL
SELECT releases.* FROM svc JOIN releases ON releases.service_id = svc.id
WHERE releases.number = (SELECT max(latest.number) FROM releases latest WHERE latest.service_id = svc.id);

-- name: ListEnvironmentActiveReleases :many
-- The active release number of every service in an environment that has one.
SELECT releases.service_id, releases.number FROM services
JOIN releases ON releases.service_id = services.id AND releases.status = 'active'
WHERE services.environment_id = +?;

-- name: SetReleaseLogsServer :exec
-- Records the server holding a release's log archive (ADR 0046).
UPDATE releases SET logs_server_id = ? WHERE id = +?;
