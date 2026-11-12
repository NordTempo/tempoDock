-- Tracing over OTLP: what a trace's spans name by ID.

-- ListServiceNamesByIDs names services with their environments and
-- projects, for a trace that crosses services (sqlc.slice: one query for
-- all of them).
-- name: ListServiceNamesByIDs :many
SELECT services.id, services.name, environments.name AS environment, projects.name AS project
FROM services
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE services.id IN (sqlc.slice(ids));

-- ListReleasesByIDs is the releases a page of spans names: their numbers,
-- and the secret values masked for readers who can't reveal them
-- (sqlc.slice: one query).
-- name: ListReleasesByIDs :many
SELECT * FROM releases WHERE id IN (sqlc.slice(ids));

-- ListReleaseServices is the service of each release named: the trace
-- forwarder keeps only spans whose release is their service's.
-- name: ListReleaseServices :many
SELECT id, service_id FROM releases WHERE id IN (sqlc.slice(ids));

-- name: GetMetricDashboard :one
-- A service's pinned charts; no row is Dock's defaults.
SELECT * FROM metric_dashboards WHERE service_id = +?;

-- name: SetMetricDashboard :exec
INSERT INTO metric_dashboards (service_id, charts, updated_by, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (service_id) DO UPDATE SET charts = excluded.charts, updated_by = excluded.updated_by, updated_at = excluded.updated_at;
