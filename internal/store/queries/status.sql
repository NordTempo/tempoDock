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
-- Every page's slug and verified hostname, for the hostnames Dock holds
-- and serves (a few rows per org, read when Dock starts and when a page
-- changes).
SELECT id, slug, published, CAST(CASE WHEN verified_at IS NULL THEN '' ELSE coalesce(hostname, '') END AS TEXT) AS hostname
FROM status_pages;

-- name: UpdateStatusPage :one
UPDATE status_pages SET title = ?, published = ?, show_uptime = ?, drafts = ? WHERE id = +?
RETURNING *;

-- name: DeleteStatusPage :exec
-- Its components and their sources go with it (ON DELETE CASCADE).
DELETE FROM status_pages WHERE id = +?;

-- name: GetStatusPageByOldSlug :one
-- An organization's page by a name it had before (ADR 0073, renaming),
-- asked only when no page has the name now.
SELECT * FROM status_pages
WHERE org_id = +? AND id = (SELECT o.page_id FROM status_page_old_slugs o WHERE o.slug = +?);

-- name: SetStatusPageSlug :one
UPDATE status_pages SET slug = ? WHERE id = +?
RETURNING *;

-- name: ReleaseStatusOldSlug :exec
-- A page takes slug, so it is no longer an old name of any page.
DELETE FROM status_page_old_slugs WHERE slug = +?;

-- name: AddStatusOldSlug :exec
INSERT INTO status_page_old_slugs (slug, page_id, changed_at) VALUES (?, ?, ?);

-- name: TrimStatusOldSlugs :exec
-- Keeps a page's newest keep old names.
DELETE FROM status_page_old_slugs
WHERE status_page_old_slugs.page_id = +sqlc.arg(page_id) AND status_page_old_slugs.slug NOT IN (
    SELECT k.slug FROM status_page_old_slugs k WHERE k.page_id = +sqlc.arg(page_id)
    ORDER BY k.changed_at DESC LIMIT +sqlc.arg(keep)
);

-- name: ListStatusOldSlugs :many
-- Every page's old names, for the addresses that redirect (as few as
-- ListStatusPageHosts' rows, read with them).
SELECT slug, page_id FROM status_page_old_slugs;

-- name: ListStatusComponents :many
SELECT * FROM status_components WHERE page_id = +? ORDER BY position;

-- name: UpdateStatusComponent :exec
-- A component kept when a page's list is written, so the incidents
-- that name it keep doing so.
UPDATE status_components SET position = ?, grp = ?, name = ?, description = ?, auto = ? WHERE id = +?;

-- name: DeleteStatusComponent :exec
DELETE FROM status_components WHERE id = +?;

-- name: DeleteStatusSources :exec
-- A kept component's sources, before its new ones are written.
DELETE FROM status_sources WHERE component_id = +?;

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

-- name: CreateStatusIncident :one
INSERT INTO status_incidents (page_id, public_id, kind, title, state, draft, starts_at, ends_at, published_at, alert_id, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetStatusIncident :one
-- An incident by its public ID, with its page.
SELECT sqlc.embed(status_incidents), sqlc.embed(status_pages)
FROM status_incidents JOIN status_pages ON status_pages.id = status_incidents.page_id
WHERE status_incidents.public_id = +?;

-- name: GetStatusIncidentByID :one
SELECT sqlc.embed(status_incidents), sqlc.embed(status_pages)
FROM status_incidents JOIN status_pages ON status_pages.id = status_incidents.page_id
WHERE status_incidents.id = +?;

-- name: UpdateStatusIncident :one
UPDATE status_incidents SET title = ?, state = ?, draft = ?, starts_at = ?, ends_at = ?, published_at = ?, resolved_at = ? WHERE id = +?
RETURNING *;

-- name: DeleteStatusIncident :exec
-- Its impacts and updates go with it (ON DELETE CASCADE).
DELETE FROM status_incidents WHERE id = +?;

-- name: ListStatusIncidents :many
-- A page's incidents open or resolved since since, newest first, with
-- the alert a draft came from (private: for the editor).
SELECT sqlc.embed(i),
  CAST(coalesce(a.kind, '') AS TEXT) AS alert_kind, CAST(coalesce(a.title, '') AS TEXT) AS alert_title,
  CAST(coalesce(a.project, '') AS TEXT) AS alert_project, CAST(coalesce(a.environment, '') AS TEXT) AS alert_environment,
  CAST(coalesce(a.service, '') AS TEXT) AS alert_service, a.project_id AS alert_project_id
FROM status_incidents AS i LEFT JOIN alerts AS a ON a.id = i.alert_id
WHERE i.page_id = +sqlc.arg(page_id) AND (i.resolved_at IS NULL OR i.resolved_at >= +sqlc.arg(since))
ORDER BY i.starts_at DESC, i.id DESC;

-- name: ListStatusImpacts :many
-- What the incidents ListStatusIncidents returns do to components.
SELECT ic.incident_id, ic.component_id, ic.impact, sc.name
FROM status_incidents AS i
JOIN status_incident_components AS ic ON ic.incident_id = i.id
JOIN status_components AS sc ON sc.id = ic.component_id
WHERE i.page_id = +sqlc.arg(page_id) AND (i.resolved_at IS NULL OR i.resolved_at >= +sqlc.arg(since))
ORDER BY sc.position;

-- name: ListStatusUpdates :many
-- The updates of the incidents ListStatusIncidents returns, oldest first.
SELECT u.*
FROM status_incidents AS i JOIN status_updates AS u ON u.incident_id = i.id
WHERE i.page_id = +sqlc.arg(page_id) AND (i.resolved_at IS NULL OR i.resolved_at >= +sqlc.arg(since))
ORDER BY u.at, u.id;

-- name: ListIncidentImpacts :many
SELECT ic.component_id, ic.impact, sc.name
FROM status_incident_components AS ic JOIN status_components AS sc ON sc.id = ic.component_id
WHERE ic.incident_id = +?
ORDER BY sc.position;

-- name: ListIncidentUpdates :many
-- An incident's updates, oldest first.
SELECT * FROM status_updates WHERE incident_id = +? ORDER BY at, id;

-- name: DeleteIncidentImpacts :exec
DELETE FROM status_incident_components WHERE incident_id = +?;

-- name: CreateIncidentImpact :exec
INSERT INTO status_incident_components (incident_id, component_id, impact) VALUES (?, ?, ?);

-- name: CreateStatusUpdate :one
INSERT INTO status_updates (incident_id, state, body, at, created_by) VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetStatusUpdate :one
SELECT * FROM status_updates WHERE id = +? AND incident_id = +?;

-- name: EditStatusUpdate :one
UPDATE status_updates SET body = ?, edited_at = ? WHERE id = +?
RETURNING *;

-- name: ListStatusHistory :many
-- A page's published incidents, newest first, for its history.
SELECT public_id, title, state, starts_at, resolved_at
FROM status_incidents
WHERE page_id = +sqlc.arg(page_id) AND state != 'draft'
ORDER BY starts_at DESC, id DESC
LIMIT +sqlc.arg(max);

-- name: ListStatusFeed :many
-- A page's newest posted updates, for its Atom feed.
SELECT i.public_id, i.title, u.state, u.body, u.at, u.edited_at
FROM status_incidents AS i JOIN status_updates AS u ON u.incident_id = i.id
WHERE i.page_id = +sqlc.arg(page_id)
ORDER BY u.at DESC, u.id DESC
LIMIT +sqlc.arg(max);

-- name: StatusDraftTargets :many
-- The components a service or uptime check feeds on published pages
-- that take drafts, each with whether an open incident names it: where
-- an alert about them drafts an incident. A component may come twice.
SELECT sp.id AS page_id, sp.org_id, sp.slug, sc.id AS component_id, sc.name,
  CAST(EXISTS (SELECT 1 FROM status_incident_components AS ic JOIN status_incidents AS i ON i.id = ic.incident_id
    WHERE ic.component_id = sc.id AND i.resolved_at IS NULL) AS BOOLEAN) AS covered
FROM status_sources AS src
JOIN status_components AS sc ON sc.id = src.component_id
JOIN status_pages AS sp ON sp.id = sc.page_id
WHERE (src.service_id = +sqlc.arg(service_id) OR src.check_id = +sqlc.arg(check_id)) AND sp.published AND sp.drafts AND sc.auto
ORDER BY sp.id, sc.position;

-- name: DeleteAlertDrafts :many
-- The drafts Dock opened from an alert, once it resolved first.
DELETE FROM status_incidents WHERE alert_id = +? AND state = 'draft'
RETURNING page_id, public_id, title;

-- name: ListMaintenanceWindows :many
-- Every maintenance window not finished yet, one row per source of its
-- components (sources NULL for a component without any): what Dock
-- holds in memory to start and end windows and to mute alerts and
-- probes during them. Ordered as status_incidents_maintenance is, so it
-- is read through it; a window's rows come together.
SELECT i.id, i.page_id, i.state, i.starts_at, i.ends_at,
  src.service_id, src.check_id
FROM status_incidents AS i
LEFT JOIN status_incident_components AS ic ON ic.incident_id = i.id
LEFT JOIN status_sources AS src ON src.component_id = ic.component_id
WHERE i.kind = 'maintenance' AND i.resolved_at IS NULL
ORDER BY i.starts_at, i.id;

-- name: SetStatusPageHostname :one
-- A page's custom hostname (NULL: none), with its verification token
-- and when it was verified (NULL until it is).
UPDATE status_pages SET hostname = ?, verify_token = ?, verified_at = ? WHERE id = +?
RETURNING *;

-- name: VerifyStatusPageHostname :execrows
-- Marks a page's hostname verified, unless it changed or was verified
-- meanwhile.
UPDATE status_pages SET verified_at = ? WHERE id = +? AND hostname = +? AND verified_at IS NULL;

-- name: StatusHostnameVerified :one
-- Whether a page holds hostname verified.
SELECT EXISTS (SELECT 1 FROM status_pages WHERE hostname = +? AND verified_at IS NOT NULL);

-- name: CreateStatusHook :one
INSERT INTO status_hooks (page_id, name, kind, target, config, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListStatusHooks :many
-- A page's hooks, by name: a handful, read when an update is posted.
SELECT * FROM status_hooks WHERE page_id = +? ORDER BY name;

-- name: GetStatusHook :one
SELECT * FROM status_hooks WHERE page_id = +? AND name = +?;

-- name: GetStatusHookByID :one
SELECT * FROM status_hooks WHERE id = +?;

-- name: DeleteStatusHook :exec
DELETE FROM status_hooks WHERE id = +?;

-- name: SetStatusHookSent :exec
UPDATE status_hooks SET last_sent_at = ? WHERE id = +?;

-- name: SetStatusHookFailed :exec
UPDATE status_hooks SET last_error = ?, last_error_at = ? WHERE id = +?;
