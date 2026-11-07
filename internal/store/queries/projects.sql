-- name: CreateServer :one
INSERT INTO servers (name, accepts_production, accepts_non_production, builder, local, status, last_seen_at, version)
VALUES (?, ?, ?, ?, ?, 'online', ?, ?)
RETURNING *;

-- name: TouchLocalServer :one
UPDATE servers SET builder = ?, status = 'online', last_seen_at = ?, version = ? WHERE id = +?
RETURNING *;

-- name: GetLocalServer :one
SELECT * FROM servers WHERE local = 1 ORDER BY id LIMIT 1;

-- name: GetServer :one
SELECT * FROM servers WHERE id = +?;

-- name: GetServerByName :one
SELECT * FROM servers WHERE name = +?;

-- name: ListServers :many
SELECT * FROM servers ORDER BY id;

-- name: UpdateServerPlacement :one
UPDATE servers SET accepts_production = ?, accepts_non_production = ?, disabled_for_placement = ?
WHERE id = +?
RETURNING *;

-- name: RenameServer :one
-- Renames a server. Everything else refers to it by ID; the unique index
-- on name refuses a name another server has.
UPDATE servers SET name = sqlc.arg(name) WHERE id = +sqlc.arg(id)
RETURNING *;

-- name: RenameServerTunnel :exec
-- Keeps the name a server's Cloudflare tunnel row shows in messages in
-- step with a rename, once the tunnel exists: until then makeTunnel
-- finds an interrupted creation by a name made from server_name.
UPDATE cloudflare_tunnels SET server_name = sqlc.arg(name) WHERE server_id = +sqlc.arg(server_id) AND tunnel_id != '';

-- name: SetServerMemory :exec
UPDATE servers SET memory_bytes = ? WHERE id = +? AND memory_bytes != +?;

-- ListServerLoads is everything placement weighs, for every server in one
-- query: memory reserved by its live services (their limit, or
-- default_memory_mb without one) and how many live projects are placed on it.
-- name: ListServerLoads :many
SELECT
    sqlc.embed(servers),
    CAST((SELECT coalesce(sum(CASE WHEN sv.memory_mb > 0 THEN sv.memory_mb ELSE sqlc.arg(default_memory_mb) END), 0)
     FROM services sv JOIN environments e ON e.id = sv.environment_id
     WHERE e.server_id = servers.id AND sv.archived_at IS NULL) AS INTEGER) AS reserved_mb,
    CAST((SELECT count(DISTINCT pp.project_id)
     FROM project_placements pp JOIN projects p ON p.id = pp.project_id
     WHERE pp.server_id = servers.id AND p.archived_at IS NULL) AS INTEGER) AS projects,
    CAST((SELECT count(*) FROM environments e WHERE e.server_id = servers.id) AS INTEGER) AS environments
FROM servers ORDER BY servers.id;

-- name: GetPlacement :one
SELECT * FROM project_placements WHERE project_id = +? AND kind = +?;

-- name: ListPlacements :many
SELECT * FROM project_placements WHERE project_id = +? ORDER BY kind;

-- name: ListServerPlacements :many
SELECT sqlc.embed(project_placements), projects.name AS project_name
FROM project_placements JOIN projects ON projects.id = project_placements.project_id
WHERE project_placements.server_id = +? AND projects.archived_at IS NULL
ORDER BY projects.name, project_placements.kind;

-- name: UpsertPlacement :exec
INSERT INTO project_placements (project_id, kind, server_id, pinned) VALUES (?, ?, ?, ?)
ON CONFLICT (project_id, kind) DO UPDATE SET server_id = excluded.server_id, pinned = excluded.pinned;

-- name: CountKindEnvironments :one
SELECT count(*) FROM environments WHERE project_id = +? AND kind = +? AND archived_at IS NULL;

-- name: ListServerEnvironments :many
SELECT environments.project_id, environments.name, environments.kind
FROM environments JOIN projects ON projects.id = environments.project_id
WHERE environments.server_id = +? AND projects.archived_at IS NULL AND environments.archived_at IS NULL
ORDER BY environments.project_id, environments.name;

-- name: CreateProject :one
INSERT INTO projects (org_id, name) VALUES (?, ?) RETURNING *;

-- name: GetProjectByName :one
SELECT * FROM projects WHERE org_id = +? AND name = +?;

-- name: GetProject :one
SELECT * FROM projects WHERE id = +?;

-- name: ListProjects :many
SELECT * FROM projects WHERE org_id = +? ORDER BY name;

-- name: CreateEnvironment :one
INSERT INTO environments (project_id, name, kind, server_id) VALUES (?, ?, ?, ?) RETURNING *;

-- name: GetEnvironmentByName :one
SELECT * FROM environments WHERE project_id = +? AND name = +?;

-- name: GetEnvironment :one
SELECT * FROM environments WHERE id = +?;

-- name: ListEnvironments :many
-- A project's environments, archived ones (closed previews) left out.
SELECT * FROM environments WHERE project_id = +? AND archived_at IS NULL ORDER BY name;

-- SetEnvironmentLock locks (locked_at set) or unlocks (NULL) an
-- environment, or opens a locked one until open_until (ADR 0044).
-- name: SetEnvironmentLock :one
UPDATE environments SET locked_at = ?, locked_by = ?, lock_reason = ?, open_until = ?
WHERE id = +? RETURNING *;

-- name: ArchiveProject :exec
UPDATE projects SET archived_at = ? WHERE id = +? AND archived_at IS NULL;

-- name: RenameProject :one
UPDATE projects SET name = ? WHERE id = +? RETURNING *;

-- ListEnvironmentLocks lists every locked environment of a live project
-- with its org, for refusing tokens account changes while one holds (ADR
-- 0044). Rare admin calls on a small table: a scan, no index.
-- name: ListEnvironmentLocks :many
SELECT p.org_id, e.locked_at, e.open_until
FROM environments e JOIN projects p ON p.id = e.project_id
WHERE e.locked_at IS NOT NULL AND p.archived_at IS NULL;
