-- ListStatsTargets lists the containers of active releases, with their
-- service, environment and server: what the stats collector samples. By
-- server, then environment, so usage events group by environment.
-- name: ListStatsTargets :many
SELECT releases.container_id, releases.service_id, environments.server_id,
  services.environment_id, environments.project_id
FROM releases
JOIN services ON services.id = releases.service_id
JOIN environments ON environments.id = services.environment_id
WHERE releases.status = 'active' AND releases.container_id != ''
ORDER BY environments.server_id, services.environment_id;

-- ListStatsNames names the services ListStatsTargets lists, for usage
-- events. Names never change, so the collector reads them only when its
-- set of services does.
-- name: ListStatsNames :many
SELECT services.id, projects.name AS project_name, environments.name AS environment_name, services.name AS service_name
FROM releases
JOIN services ON services.id = releases.service_id
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE releases.status = 'active' AND releases.container_id != '';

-- name: AddServerStats :exec
INSERT OR REPLACE INTO server_stats (res, at, server_id, cpu, memory, disk, load1) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: AddServiceStats :exec
INSERT OR REPLACE INTO service_stats (res, at, service_id, cpu, memory, io_read, io_write) VALUES (?, ?, ?, ?, ?, ?, ?);

-- DownsampleServerStats averages the 1-minute points of [from, to) into
-- one 10-minute point at from.
-- name: DownsampleServerStats :exec
INSERT OR REPLACE INTO server_stats (res, at, server_id, cpu, memory, disk, load1)
SELECT 600, sqlc.arg(from_at), m.server_id, CAST(avg(m.cpu) AS INTEGER), CAST(avg(m.memory) AS INTEGER), max(m.disk), CAST(avg(m.load1) AS INTEGER)
FROM server_stats AS m WHERE m.res = 60 AND m.at >= +sqlc.arg(from_at) AND m.at < +sqlc.arg(to_at)
GROUP BY m.server_id;

-- name: DownsampleServiceStats :exec
INSERT OR REPLACE INTO service_stats (res, at, service_id, cpu, memory, io_read, io_write)
SELECT 600, sqlc.arg(from_at), m.service_id, CAST(avg(m.cpu) AS INTEGER), CAST(avg(m.memory) AS INTEGER), CAST(avg(m.io_read) AS INTEGER), CAST(avg(m.io_write) AS INTEGER)
FROM service_stats AS m WHERE m.res = 60 AND m.at >= +sqlc.arg(from_at) AND m.at < +sqlc.arg(to_at)
GROUP BY m.service_id;

-- name: PruneServerStats :exec
DELETE FROM server_stats WHERE res = +? AND at < +?;

-- name: PruneServiceStats :exec
DELETE FROM service_stats WHERE res = +? AND at < +?;

-- name: ListServerStats :many
SELECT at, cpu, memory, disk, load1 FROM server_stats
WHERE res = +? AND at >= +? AND server_id = +? ORDER BY at;

-- name: ListServiceStats :many
SELECT at, cpu, memory, io_read, io_write FROM service_stats
WHERE res = +? AND at >= +? AND service_id = +? ORDER BY at;
