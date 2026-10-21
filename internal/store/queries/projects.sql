-- name: CreateServer :one
INSERT INTO servers (name, role, builder, local, status, last_seen_at)
VALUES (?, ?, ?, ?, 'online', ?)
RETURNING *;

-- name: TouchLocalServer :one
UPDATE servers SET builder = ?, status = 'online', last_seen_at = ? WHERE id = ?
RETURNING *;

-- name: GetLocalServer :one
SELECT * FROM servers WHERE local = 1 ORDER BY id LIMIT 1;

-- name: GetServer :one
SELECT * FROM servers WHERE id = ?;

-- name: ListServers :many
SELECT * FROM servers ORDER BY id;

-- name: CountEnvironmentsByServer :many
SELECT server_id, count(*) AS environments FROM environments GROUP BY server_id;

-- name: CreateProject :one
INSERT INTO projects (name) VALUES (?) RETURNING *;

-- name: GetProjectByName :one
SELECT * FROM projects WHERE name = ?;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY name;

-- name: CreateEnvironment :one
INSERT INTO environments (project_id, name, server_id) VALUES (?, ?, ?) RETURNING *;

-- name: GetEnvironmentByName :one
SELECT * FROM environments WHERE project_id = ? AND name = ?;

-- name: GetEnvironment :one
SELECT * FROM environments WHERE id = ?;

-- name: ListEnvironments :many
SELECT * FROM environments WHERE project_id = ? ORDER BY name;

-- name: ArchiveProject :exec
UPDATE projects SET archived_at = ? WHERE id = ? AND archived_at IS NULL;
