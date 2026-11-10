-- Public status pages (ADR 0073).

-- name: CreateStatusPage :one
INSERT INTO status_pages (org_id, slug, title, published, show_uptime, created_at, created_by)
VALUES (?, ?, ?, 0, 1, ?, ?)
RETURNING *;

-- name: GetStatusPage :one
-- An organization's page by its slug.
SELECT * FROM status_pages WHERE org_id = +? AND slug = +?;

-- name: GetStatusPageByID :one
SELECT * FROM status_pages WHERE id = +?;

-- name: StatusSlugTaken :one
-- Whether any org's page holds slug.
SELECT EXISTS (SELECT 1 FROM status_pages WHERE slug = +?);

-- name: ListStatusPages :many
SELECT * FROM status_pages WHERE org_id = +? ORDER BY slug;

-- name: CountOrgStatusPages :one
SELECT count(*) FROM status_pages WHERE org_id = +?;

-- name: ListStatusPageHosts :many
-- Every page's slug, for the hostnames Dock holds and serves (a few rows
-- per org, read when Dock starts and when a page changes).
SELECT id, slug, published FROM status_pages;

-- name: UpdateStatusPage :one
UPDATE status_pages SET title = ?, published = ?, show_uptime = ? WHERE id = +?
RETURNING *;

-- name: DeleteStatusPage :exec
-- Its components and their sources go with it (ON DELETE CASCADE).
DELETE FROM status_pages WHERE id = +?;

-- name: ListStatusComponents :many
SELECT * FROM status_components WHERE page_id = +? ORDER BY position;

-- name: DeleteStatusComponents :exec
-- A page's components, before its new list is written.
DELETE FROM status_components WHERE page_id = +?;

-- name: CreateStatusComponent :one
INSERT INTO status_components (page_id, position, grp, name, description, auto)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: CreateStatusSource :exec
INSERT INTO status_sources (component_id, service_id, check_id) VALUES (?, ?, ?);

-- name: ListStatusPageSources :many
-- A page's sources with the service each stands for (a check's own
-- service for a check source), where it runs and whether it is a
-- preview, and the uptime checks it covers: one row per check, or one
-- row with check_id 0 for a service without any.
SELECT src.id AS source_id, src.component_id, src.check_id AS source_check,
  sv.id AS service_id, sv.name AS service, sv.archived_at, p.archived_at AS project_archived_at,
  e.id AS environment_id, e.name AS environment, p.id AS project_id, p.name AS project, p.org_id,
  CAST(EXISTS (SELECT 1 FROM previews WHERE previews.environment_id = e.id) AS BOOLEAN) AS preview,
  CAST(coalesce(one.id, uc.id, 0) AS INTEGER) AS check_id
FROM status_components AS sc
JOIN status_sources AS src ON src.component_id = sc.id
LEFT JOIN uptime_checks AS one ON one.id = src.check_id
JOIN services AS sv ON sv.id = coalesce(src.service_id, one.service_id)
JOIN environments AS e ON e.id = sv.environment_id
JOIN projects AS p ON p.id = e.project_id
LEFT JOIN uptime_checks AS uc ON src.service_id IS NOT NULL AND uc.service_id = src.service_id
WHERE sc.page_id = +sqlc.arg(page_id)
ORDER BY src.id;

-- name: StatusPageDays :many
-- A page's components' probes per UTC day since since: day rows before
-- day_from (the first day not rolled into one), hour rows from it until
-- minute_from (the first minute not rolled into an hour) and minute
-- rows after, each part read through uptime_stats' key; a day may come
-- in more than one row, which the caller adds up. A check a component
-- names twice (its service and itself) counts once.
WITH checks AS (
  SELECT src.component_id, src.check_id AS check_id
  FROM status_components AS sc JOIN status_sources AS src ON src.component_id = sc.id
  WHERE sc.page_id = +sqlc.arg(page_id) AND src.check_id IS NOT NULL
  UNION
  SELECT src.component_id, uc.id AS check_id
  FROM status_components AS sc JOIN status_sources AS src ON src.component_id = sc.id
  JOIN uptime_checks AS uc ON uc.service_id = src.service_id
  WHERE sc.page_id = +sqlc.arg(page_id)
)
SELECT checks.component_id, CAST(s.at AS INTEGER) AS day, CAST(sum(s.up) AS INTEGER) AS up, CAST(sum(s.total) AS INTEGER) AS total
FROM checks CROSS JOIN uptime_stats AS s
  ON s.check_id = checks.check_id AND s.res = 86400 AND s.at >= +sqlc.arg(since) AND s.at < +sqlc.arg(day_from)
GROUP BY checks.component_id, s.at
UNION ALL
SELECT checks.component_id, CAST(s.at / 86400 * 86400 AS INTEGER) AS day, CAST(sum(s.up) AS INTEGER), CAST(sum(s.total) AS INTEGER)
FROM checks CROSS JOIN uptime_stats AS s
  ON s.check_id = checks.check_id AND s.res = 3600 AND s.at >= +sqlc.arg(day_from) AND s.at < +sqlc.arg(minute_from)
GROUP BY checks.component_id, day
UNION ALL
SELECT checks.component_id, CAST(s.at / 86400 * 86400 AS INTEGER) AS day, CAST(sum(s.up) AS INTEGER), CAST(sum(s.total) AS INTEGER)
FROM checks CROSS JOIN uptime_stats AS s
  ON s.check_id = checks.check_id AND s.res = 60 AND s.at >= +sqlc.arg(minute_from)
GROUP BY checks.component_id, day;

-- name: ListStatusSourceCandidates :many
-- Every live service of an organization's live projects that isn't a
-- preview, with each of its uptime checks (one row per check, check_id 0
-- for a service without any): what a component may be built from.
SELECT sv.id AS service_id, e.id AS environment_id, p.id AS project_id,
  p.name AS project, e.name AS environment, sv.name AS service,
  CAST(coalesce(uc.id, 0) AS INTEGER) AS check_id, CAST(coalesce(uc.url, '') AS TEXT) AS check_url
FROM projects AS p
JOIN environments AS e ON e.project_id = p.id
JOIN services AS sv ON sv.environment_id = e.id
LEFT JOIN uptime_checks AS uc ON uc.service_id = sv.id
WHERE p.org_id = +sqlc.arg(org_id) AND p.archived_at IS NULL AND sv.archived_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM previews WHERE previews.environment_id = e.id)
ORDER BY p.name, e.name, sv.name, uc.id;

-- name: CountStatusComponents :many
-- How many components each of an org's pages has.
SELECT sc.page_id, count(*) AS components
FROM status_pages AS sp JOIN status_components AS sc ON sc.page_id = sp.id
WHERE sp.org_id = +? GROUP BY sc.page_id;
