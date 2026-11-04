-- name: AddLogDrain :one
INSERT INTO log_drains (environment_id, kind, name, target, config, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListLogDrains :many
-- An environment's drains, by name.
SELECT * FROM log_drains WHERE environment_id = +? ORDER BY name;

-- name: ListAllLogDrains :many
-- Every drain, read once when the daemon starts.
SELECT * FROM log_drains ORDER BY id;

-- name: GetLogDrain :one
SELECT * FROM log_drains WHERE id = +? AND environment_id = +?;

-- name: DeleteLogDrain :exec
DELETE FROM log_drains WHERE id = +?;

-- name: ListDrainContainers :many
-- The active release of each service in an environment that has a
-- container, with what its lines are labelled with: what log drains
-- follow when a drain is added, the daemon starts or a server reconnects.
-- The release's config holds the secret values its lines are masked of.
SELECT sqlc.embed(releases), services.name AS service, environments.name AS environment,
       environments.server_id, projects.name AS project, servers.name AS server
FROM services
JOIN releases ON releases.service_id = services.id AND releases.status = 'active'
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
JOIN servers ON servers.id = environments.server_id
WHERE services.environment_id = +? AND releases.container_id != '';

-- name: GetLogSource :one
-- A release with what its log lines are labelled with when drained, and
-- where its service runs.
SELECT sqlc.embed(releases), services.name AS service, environments.id AS environment_id,
       environments.name AS environment, environments.server_id, projects.id AS project_id,
       projects.name AS project, servers.name AS server
FROM releases
JOIN services ON services.id = releases.service_id
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
JOIN servers ON servers.id = environments.server_id
WHERE releases.id = +?;
