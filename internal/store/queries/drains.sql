-- Server drains and forwarding (ADR 0063).

-- name: CreateServerDrain :one
INSERT INTO server_drains (server_id, kind, max_hold_s, created_by, started_at) VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: AddServerDrainItem :exec
INSERT INTO server_drain_items (drain_id, environment_id, to_server_id, position) VALUES (?, ?, ?, ?);

-- name: GetServerDrain :one
SELECT * FROM server_drains WHERE id = +?;

-- name: GetLatestServerDrain :one
SELECT * FROM server_drains WHERE server_id = +? ORDER BY id DESC LIMIT 1;

-- name: GetOpenServerDrain :one
-- A server's drain that is running or paused, if any.
SELECT * FROM server_drains WHERE server_id = +? AND status IN ('running', 'paused');

-- name: ListRunningServerDrains :many
SELECT * FROM server_drains WHERE status = 'running' ORDER BY id;

-- name: ListServerDrainItems :many
-- A drain's environments in order, with their names and target's name.
SELECT sqlc.embed(server_drain_items), projects.name AS project_name, environments.name AS environment_name,
       projects.org_id AS org_id, servers.name AS to_server_name
FROM server_drain_items
JOIN environments ON environments.id = server_drain_items.environment_id
JOIN projects ON projects.id = environments.project_id
JOIN servers ON servers.id = server_drain_items.to_server_id
WHERE server_drain_items.drain_id = +?
ORDER BY server_drain_items.position;

-- name: SetServerDrainStatus :exec
UPDATE server_drains SET status = ?, error = ?, finished_at = ? WHERE id = +?;

-- name: SetServerDrainItem :exec
UPDATE server_drain_items SET status = ?, move_id = ?, error = ? WHERE drain_id = +? AND environment_id = +?;

-- name: SetServerDrainItemTarget :exec
UPDATE server_drain_items SET to_server_id = ? WHERE drain_id = +? AND environment_id = +?;

-- name: CreateServerForward :exec
INSERT INTO server_forwards (server_id, host_port, service_id, move_id, created_at) VALUES (?, ?, ?, ?, ?);

-- name: GetServerForward :one
SELECT * FROM server_forwards WHERE server_id = +? AND host_port = +?;

-- name: ListServerForwards :many
SELECT * FROM server_forwards WHERE server_id = +? ORDER BY host_port;

-- name: ListServerForwardDetails :many
-- A server's forwards with the service, environment and project they
-- stand for and the server the environment is on now, for the console.
SELECT server_forwards.host_port, server_forwards.created_at, services.name AS service_name,
       environments.name AS environment_name, projects.name AS project_name, servers.name AS now_on
FROM server_forwards
JOIN services ON services.id = server_forwards.service_id
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
JOIN servers ON servers.id = environments.server_id
WHERE server_forwards.server_id = +?
ORDER BY projects.name, environments.name, services.name;

-- name: DeleteEnvironmentForwardsOn :exec
-- Ends a server's forwards of an environment's services: it moved back
-- there.
DELETE FROM server_forwards WHERE server_id = +sqlc.arg(server_id) AND service_id IN (
    SELECT id FROM services WHERE environment_id = +sqlc.arg(environment_id)
);

-- name: DeleteServerForwards :exec
DELETE FROM server_forwards WHERE server_id = +?;

-- name: DeleteServiceForwards :exec
DELETE FROM server_forwards WHERE service_id = +?;

-- name: ListForwardedDomains :many
-- The verified domains of the services a server forwards, with the
-- forward's port as host port and the rest like ListRoutableDomains.
SELECT domains.hostname, domains.redirect_to, domains.service_id,
       services.name AS service_name,
       CAST(services.stopped_at IS NOT NULL AS BOOLEAN) AS stopped,
       environments.id AS environment_id,
       CAST(environments.asleep_at IS NOT NULL AS BOOLEAN) AS asleep,
       CAST(environments.sleep_after_minutes > 0 OR environments.asleep_at IS NOT NULL AS BOOLEAN) AS sleepable,
       server_forwards.host_port,
       CAST(coalesce((SELECT releases.id FROM releases
                      WHERE releases.service_id = services.id AND releases.status = 'active'), 0) AS INTEGER) AS release_id,
       CAST(coalesce(service_access.mode, '') AS TEXT) AS access_mode,
       CAST(coalesce(service_access.username, '') AS TEXT) AS access_username,
       CAST(coalesce(service_access.password_hash, '') AS TEXT) AS access_password_hash,
       service_access.key AS access_key
FROM server_forwards
JOIN services ON services.id = server_forwards.service_id
JOIN environments ON environments.id = services.environment_id
JOIN domains ON domains.service_id = services.id
LEFT JOIN service_access ON service_access.service_id = services.id
WHERE server_forwards.server_id = +? AND services.archived_at IS NULL AND domains.verified_at IS NOT NULL
ORDER BY domains.service_id, domains.id;

-- name: CountServerForwards :one
SELECT count(*) FROM server_forwards WHERE server_id = +?;

-- A removed server takes the history that names it: its drains, the
-- moves to and from it and its snapshots (whose files went with it).
-- The audit log keeps the record. RemoveServer runs these in order.

-- name: DeleteServerDrainItems :exec
DELETE FROM server_drain_items
WHERE to_server_id = +sqlc.arg(server_id) OR drain_id IN (SELECT id FROM server_drains WHERE server_id = +sqlc.arg(server_id));

-- name: UnlinkServerMovesFromDrains :exec
UPDATE server_drain_items SET move_id = NULL WHERE move_id IN (
    SELECT id FROM environment_moves WHERE from_server_id = +sqlc.arg(server_id) OR to_server_id = +sqlc.arg(server_id)
);

-- name: DeleteServerMoveServices :exec
DELETE FROM environment_move_services WHERE move_id IN (
    SELECT id FROM environment_moves WHERE from_server_id = +sqlc.arg(server_id) OR to_server_id = +sqlc.arg(server_id)
);

-- name: DeleteServerMoves :exec
DELETE FROM environment_moves WHERE from_server_id = +sqlc.arg(server_id) OR to_server_id = +sqlc.arg(server_id);

-- name: DeleteServerDrains :exec
DELETE FROM server_drains WHERE server_id = +?;

-- name: DeleteServerSnapshots :exec
DELETE FROM volume_snapshots WHERE server_id = +?;

-- name: ListDrainableEnvironments :many
-- The environments on a server a drain moves, with their project and
-- org, in the order it moves them: by org, project, kind and name.
SELECT environments.id, environments.name, environments.kind, environments.project_id,
       projects.name AS project_name, orgs.slug AS org_slug
FROM environments
JOIN projects ON projects.id = environments.project_id
JOIN orgs ON orgs.id = projects.org_id
WHERE environments.server_id = +? AND environments.archived_at IS NULL AND projects.archived_at IS NULL
ORDER BY orgs.slug, projects.name, environments.kind, environments.name;

-- name: ListOpenServerDrainProgress :many
-- Every running or paused drain with how many of its environments are
-- done (moved or skipped), for the server list.
SELECT server_drains.id, server_drains.server_id, server_drains.status, server_drains.error,
       CAST((SELECT count(*) FROM server_drain_items WHERE server_drain_items.drain_id = server_drains.id) AS INTEGER) AS total,
       CAST((SELECT count(*) FROM server_drain_items WHERE server_drain_items.drain_id = server_drains.id
                AND server_drain_items.status IN ('moved', 'skipped')) AS INTEGER) AS done
FROM server_drains WHERE server_drains.status IN ('running', 'paused');

-- name: ListServiceForwardServers :many
-- The servers that forward a service that moved away from them.
SELECT server_id FROM server_forwards WHERE service_id = +? ORDER BY server_id;
