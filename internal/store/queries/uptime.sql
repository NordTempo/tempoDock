-- Uptime checks (ADR 0055).

-- name: CreateUptimeCheck :one
INSERT INTO uptime_checks (service_id, url, expect_status, interval_s, timeout_ms, created_at, created_by, auto)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetUptimeCheck :one
SELECT * FROM uptime_checks WHERE id = +?;

-- name: UpdateUptimeCheck :one
-- A check's interval and expected status, changed in place.
UPDATE uptime_checks SET interval_s = ?, expect_status = ? WHERE id = +?
RETURNING *;

-- name: DeleteUptimeCheck :exec
-- Its statistics go with it (ON DELETE CASCADE).
DELETE FROM uptime_checks WHERE id = +?;

-- name: ListServiceUptimeChecks :many
SELECT * FROM uptime_checks WHERE service_id = +? ORDER BY id;

-- name: ListUptimeSchedule :many
-- Every check the scheduler probes: those of live services in live
-- projects, with where their service runs and its names for alerts and
-- events.
SELECT uptime_checks.id, uptime_checks.service_id, uptime_checks.url, uptime_checks.expect_status,
  uptime_checks.interval_s, uptime_checks.timeout_ms,
  environments.server_id, environments.id AS environment_id, projects.id AS project_id, projects.org_id,
  projects.name AS project, environments.name AS environment, services.name AS service
FROM uptime_checks
JOIN services ON services.id = uptime_checks.service_id
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE services.archived_at IS NULL AND projects.archived_at IS NULL;

-- name: AddUptimeProbe :exec
-- Adds one probe to its check's minute row.
INSERT INTO uptime_stats (check_id, res, at, up, total, latency_ms_sum) VALUES (?, 60, ?, ?, 1, ?)
ON CONFLICT (check_id, res, at) DO UPDATE SET
  up = uptime_stats.up + excluded.up, total = uptime_stats.total + 1,
  latency_ms_sum = uptime_stats.latency_ms_sum + excluded.latency_ms_sum;

-- name: RollupUptimeHour :exec
-- Sums the minute rows of the hour [from_at, to_at) into one hour row per
-- check. Running it again for the same hour gives the same rows.
INSERT OR REPLACE INTO uptime_stats (check_id, res, at, up, total, latency_ms_sum)
SELECT m.check_id, 3600, sqlc.arg(from_at), sum(m.up), sum(m.total), sum(m.latency_ms_sum)
FROM uptime_stats AS m WHERE m.res = 60 AND m.at >= +sqlc.arg(from_at) AND m.at < +sqlc.arg(to_at)
GROUP BY m.check_id;

-- name: PruneUptimeStats :exec
DELETE FROM uptime_stats WHERE res = +? AND at < +?;

-- name: UptimeTotals :many
-- A service's checks' probes over the last day (minute rows), and over
-- the last week and month: hour rows before minute_from, the first minute
-- not yet rolled into an hour, and minute rows from it. CROSS JOIN, as in
-- UptimeSeries.
SELECT s.check_id,
  CAST(coalesce(sum(CASE WHEN s.res = 60 AND s.at >= sqlc.arg(since_day) THEN s.up END), 0) AS INTEGER) AS up_day,
  CAST(coalesce(sum(CASE WHEN s.res = 60 AND s.at >= sqlc.arg(since_day) THEN s.total END), 0) AS INTEGER) AS total_day,
  CAST(coalesce(sum(CASE WHEN s.at >= sqlc.arg(since_week) AND (s.res = 3600 OR s.at >= sqlc.arg(minute_from)) THEN s.up END), 0) AS INTEGER) AS up_week,
  CAST(coalesce(sum(CASE WHEN s.at >= sqlc.arg(since_week) AND (s.res = 3600 OR s.at >= sqlc.arg(minute_from)) THEN s.total END), 0) AS INTEGER) AS total_week,
  CAST(coalesce(sum(CASE WHEN s.res = 3600 OR s.at >= sqlc.arg(minute_from) THEN s.up END), 0) AS INTEGER) AS up_month,
  CAST(coalesce(sum(CASE WHEN s.res = 3600 OR s.at >= sqlc.arg(minute_from) THEN s.total END), 0) AS INTEGER) AS total_month
FROM uptime_checks AS c
CROSS JOIN uptime_stats AS s ON s.check_id = c.id
WHERE c.service_id = +sqlc.arg(service_id)
  AND ((s.res = 3600 AND s.at >= +sqlc.arg(since_month) AND s.at < +sqlc.arg(minute_from)) OR (s.res = 60 AND s.at >= +sqlc.arg(since_day)))
GROUP BY c.id;

-- name: UptimeSeries :many
-- A service's checks' probes since since (within the last day), in
-- 30-minute buckets. CROSS JOIN reads each check's rows through
-- uptime_stats' key instead of every check's through the time index.
SELECT s.check_id, CAST(s.at / 1800 * 1800 AS INTEGER) AS bucket,
  CAST(sum(s.up) AS INTEGER) AS up, CAST(sum(s.total) AS INTEGER) AS total
FROM uptime_checks AS c
CROSS JOIN uptime_stats AS s ON s.check_id = c.id
WHERE c.service_id = +sqlc.arg(service_id) AND s.res = 60 AND s.at >= +sqlc.arg(since)
GROUP BY s.check_id, bucket;

-- name: UptimeProjectSummary :many
-- Every check of a project's services with its probes over the last day:
-- the overview's uptime per service.
SELECT c.id, c.service_id,
  CAST(coalesce(sum(s.up), 0) AS INTEGER) AS up, CAST(coalesce(sum(s.total), 0) AS INTEGER) AS total
FROM uptime_checks AS c
JOIN services ON services.id = c.service_id
JOIN environments ON environments.id = services.environment_id
LEFT JOIN uptime_stats AS s ON s.check_id = c.id AND s.res = 60 AND s.at >= +sqlc.arg(since)
WHERE environments.project_id = +sqlc.arg(project_id)
GROUP BY c.id;

-- name: ListUptimeIncidents :many
-- The newest per_check alerts of each of a service's checks, in no
-- order (a few rows, sorted by the caller). Newer alerts of a key are
-- counted through alerts_key.
SELECT alerts.id, alerts.key, alerts.title, alerts.message, alerts.server, alerts.opened_at, alerts.resolved_at
FROM alerts
WHERE alerts.key IN (SELECT 'uptime:' || uptime_checks.id FROM uptime_checks WHERE uptime_checks.service_id = +sqlc.arg(service_id))
  AND (SELECT count(*) FROM alerts AS newer WHERE newer.key = alerts.key AND newer.id > alerts.id) < CAST(sqlc.arg(per_check) AS INTEGER);

-- name: CountOrgUptimeChecks :one
-- An organization's uptime checks, for its cap.
SELECT count(*) FROM uptime_checks
JOIN services ON services.id = uptime_checks.service_id
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE projects.org_id = +?;
