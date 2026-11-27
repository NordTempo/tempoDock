-- Image vulnerability scanning (ADR 0074).

-- name: ListUnscannedReleases :many
-- Live releases whose image has no scan yet, with what picking or
-- making one needs. Through the releases_unscanned partial index.
SELECT r.id, r.service_id, r.image_ref, r.image_id, r.image_digest, r.pinned_ref, r.built_on_server_id,
       r.base_image, r.base_digest, e.kind AS env_kind, e.server_id, p.org_id
FROM releases r
JOIN services s ON s.id = r.service_id AND s.archived_at IS NULL
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE r.status = 'active' AND r.image_scan_id IS NULL
ORDER BY r.id LIMIT +sqlc.arg(max_rows);

-- name: SetReleasePinnedRef :exec
UPDATE releases SET pinned_ref = ? WHERE id = +sqlc.arg(id);

-- name: SetReleaseImageScan :exec
UPDATE releases SET image_scan_id = ? WHERE id = +sqlc.arg(id);

-- name: GetImageScanByKey :one
SELECT * FROM image_scans WHERE image_key = +sqlc.arg(image_key);

-- name: GetImageScan :one
SELECT * FROM image_scans WHERE id = +sqlc.arg(id);

-- name: AddImageScan :one
INSERT INTO image_scans (image_key, pull_ref, local_ref, built_on_server_id, org_id, production, priority, base_image, base_digest, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: RaiseImageScanPriority :exec
-- A scan another, more important release uses now: production first.
UPDATE image_scans SET production = max(production, sqlc.arg(production)), priority = min(priority, sqlc.arg(priority))
WHERE id = +sqlc.arg(id);

-- name: NextImageScan :one
-- The next scan to run: queued, due, and one a builder there is may
-- scan (production ones only with a builder that builds production, the
-- rest with one that builds non-production).
SELECT * FROM image_scans
WHERE state = 'queued' AND (next_attempt_at IS NULL OR next_attempt_at <= +sqlc.arg(now))
  AND CASE production WHEN 0 THEN +sqlc.arg(non_production_builder) ELSE +sqlc.arg(production_builder) END = 1
ORDER BY priority, id LIMIT 1;

-- name: NextImageScanRetry :one
-- When the earliest scan waiting to retry is due (0 for none).
SELECT CAST(coalesce(min(next_attempt_at), 0) AS INTEGER) AS due FROM image_scans
WHERE state = 'queued' AND next_attempt_at > +sqlc.arg(now);

-- name: SetImageScanState :exec
UPDATE image_scans SET state = ?, error = ?, attempts = ?, next_attempt_at = ?
WHERE id = +sqlc.arg(id);

-- name: SetImageScanSBOM :exec
UPDATE image_scans SET distro = ?, packages = ?, sbom_bytes = ?, sbom_at = ?, server_id = ?
WHERE id = +sqlc.arg(id);

-- name: SetImageScanResult :exec
UPDATE image_scans SET state = 'scanned', error = '', attempts = 0, next_attempt_at = NULL,
    matched_at = ?, db_built_at = ?, critical = ?, high = ?, medium = ?, low = ?, other = ?,
    fixable = ?, kev = ?, truncated = ?, worst_fix = ?
WHERE id = +sqlc.arg(id);

-- name: RequeueImageScan :exec
-- Scan again: from its SBOM when it has one (match only), else in full.
UPDATE image_scans SET state = 'queued', attempts = 0, next_attempt_at = NULL, error = '',
    priority = min(priority, sqlc.arg(priority))
WHERE id = +sqlc.arg(id) AND state != 'scanning';

-- name: RequeueInterruptedScans :exec
-- On start: scans the last daemon stopped in the middle of run again.
UPDATE image_scans SET state = 'queued' WHERE state = 'scanning';

-- name: ListImageFindingsSeen :many
-- When each of an image's findings was first seen, kept across matches.
SELECT vuln_id, package, version, first_seen_at FROM image_findings WHERE scan_id = +sqlc.arg(scan_id);

-- name: DeleteImageFindings :exec
DELETE FROM image_findings WHERE scan_id = +sqlc.arg(scan_id);

-- name: AddImageFinding :exec
INSERT OR IGNORE INTO image_findings (scan_id, severity, vuln_id, package, version, pkg_type, fixed_in, kev, epss, first_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListImageFindings :many
-- An image's findings, worst first.
SELECT severity, vuln_id, package, version, pkg_type, fixed_in, kev, epss, first_seen_at
FROM image_findings WHERE scan_id = +sqlc.arg(scan_id)
ORDER BY severity DESC, vuln_id, package, version LIMIT +sqlc.arg(max_rows);

-- name: ListOrgImages :many
-- The org's live releases with their scans (none yet: NULL), for the
-- Images page; core groups them by scan.
SELECT r.id AS release_id, r.number, r.image_ref, r.image_scan_id, r.finished_at,
       s.id AS service_id, s.name AS service, s.auto_update, e.id AS environment_id, e.name AS environment, e.kind AS env_kind, p.id AS project_id, p.name AS project
FROM projects p
JOIN environments e ON e.project_id = p.id AND e.archived_at IS NULL
JOIN services s ON s.environment_id = e.id AND s.archived_at IS NULL
JOIN releases r ON r.service_id = s.id AND r.status = 'active'
WHERE p.org_id = +sqlc.arg(org_id) AND p.archived_at IS NULL
ORDER BY p.name, e.name, s.name;

-- name: ListImageScansByID :many
-- Scans by ID, for a page of images (sqlc.slice: one query for all).
SELECT * FROM image_scans WHERE id IN (sqlc.slice(ids));

-- name: ListImageScanReleases :many
-- The org's releases of one image, live ones first, newest first.
SELECT r.id AS release_id, r.number, r.status, r.image_ref, s.id AS service_id, s.name AS service, s.auto_update,
       e.id AS environment_id, e.name AS environment, e.kind AS env_kind, p.id AS project_id, p.name AS project
FROM releases r
JOIN services s ON s.id = r.service_id
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE r.image_scan_id = +sqlc.arg(scan_id) AND p.org_id = +sqlc.arg(org_id)
ORDER BY r.status = 'active' DESC, r.id DESC LIMIT 200;

-- name: ListProjectImageScans :many
-- The scans of a project's live releases, for the service overview.
SELECT r.service_id, i.*
FROM environments e
JOIN services s ON s.environment_id = e.id
JOIN releases r ON r.service_id = s.id AND r.status = 'active'
JOIN image_scans i ON i.id = r.image_scan_id
WHERE e.project_id = +sqlc.arg(project_id);

-- name: ListOrgFindings :many
-- Findings at or above a severity in the org's live releases, worst
-- first, with where they run: `dock vulns --all` and agents.
SELECT f.severity, f.vuln_id, f.package, f.version, f.pkg_type, f.fixed_in, f.kev, f.epss,
       s.id AS service_id, s.name AS service, e.id AS environment_id, e.name AS environment, e.kind AS env_kind, p.id AS project_id, p.name AS project, r.number
FROM projects p
JOIN environments e ON e.project_id = p.id AND e.archived_at IS NULL
JOIN services s ON s.environment_id = e.id AND s.archived_at IS NULL
JOIN releases r ON r.service_id = s.id AND r.status = 'active'
JOIN image_findings f ON f.scan_id = r.image_scan_id AND f.severity >= +sqlc.arg(min_severity)
WHERE p.org_id = +sqlc.arg(org_id) AND p.archived_at IS NULL
ORDER BY f.severity DESC, f.vuln_id LIMIT +sqlc.arg(max_rows);

-- name: ListOrphanImageScans :many
-- Scans no release refers to any more, for the retention pass.
SELECT i.id FROM image_scans i
WHERE NOT EXISTS (SELECT 1 FROM releases r WHERE r.image_scan_id = i.id)
LIMIT +sqlc.arg(max_rows);

-- name: DeleteImageScan :exec
DELETE FROM image_scans WHERE id = +sqlc.arg(id);

-- name: ClearTrimmedReleaseScan :exec
-- A release whose history is trimmed lets go of its scan.
UPDATE releases SET image_scan_id = NULL WHERE id = +sqlc.arg(id) AND image_scan_id IS NOT NULL;

-- name: CountImageScans :many
-- Scans by state and whether production uses them, for the console. One
-- row per image in use.
SELECT state, production, count(*) AS n FROM image_scans GROUP BY state, production;

-- name: SetServerScanDatabase :exec
UPDATE servers SET scan_db_built_at = ?, scan_error = ? WHERE id = +sqlc.arg(id);

-- name: GetScanning :one
SELECT scanning FROM platform_settings WHERE id = 1;

-- name: SetScanning :exec
INSERT INTO platform_settings (id, scanning, updated_by, updated_at) VALUES (1, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET scanning = excluded.scanning, updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- name: ListImageScanLiveUses :many
-- The live releases of one image in every org, for its scan events.
SELECT p.org_id, p.id AS project_id, p.name AS project, e.id AS environment_id, e.name AS environment,
       s.id AS service_id, s.name AS service, r.number, r.image_ref
FROM releases r
JOIN services s ON s.id = r.service_id AND s.archived_at IS NULL
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE r.image_scan_id = +sqlc.arg(scan_id) AND r.status = 'active';

-- name: ListImageScansLiveUses :many
-- ListImageScanLiveUses for several images at once (sqlc.slice), with the
-- scan each release runs: one read for the scans a tag check updated.
SELECT r.image_scan_id, p.org_id, p.id AS project_id, p.name AS project, e.id AS environment_id, e.name AS environment,
       s.id AS service_id, s.name AS service, r.number, r.image_ref
FROM releases r
JOIN services s ON s.id = r.service_id AND s.archived_at IS NULL
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE r.image_scan_id IN (sqlc.slice(scan_ids)) AND r.status = 'active';

-- name: ListServicesLiveUses :many
-- The live releases of some services and the scan of each (NULL before
-- it is scanned), for rechecking their vulnerability alerts at once.
SELECT p.org_id, p.id AS project_id, p.name AS project, e.id AS environment_id, e.name AS environment,
       s.id AS service_id, s.name AS service, r.number, r.image_ref, r.image_scan_id
FROM releases r
JOIN services s ON s.id = r.service_id AND s.archived_at IS NULL
JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = e.project_id
WHERE r.service_id IN (sqlc.slice(service_ids)) AND r.status = 'active';

-- Phase 2: ignores, alerts on new vulnerabilities, the daily refresh.

-- name: SetServerScanChecked :exec
-- When a builder last asked for a newer vulnerability database.
UPDATE servers SET scan_db_checked_at = ? WHERE id = +sqlc.arg(id);

-- name: RequeueStaleLiveScans :execrows
-- After a newer database: every live image matched against an older one
-- is matched again from its stored SBOM (it has one: it was scanned).
UPDATE image_scans SET state = 'queued', attempts = 0, next_attempt_at = NULL, error = ''
WHERE state = 'scanned' AND (db_built_at IS NULL OR db_built_at < +sqlc.arg(built))
  AND EXISTS (SELECT 1 FROM releases r WHERE r.image_scan_id = image_scans.id AND r.status = 'active');

-- name: ListScanFindingsOf :many
-- The findings of some images with some vulnerability IDs: those an
-- org's ignores may hide (sqlc.slice: one query for a page).
SELECT scan_id, severity, vuln_id, package, version, pkg_type, fixed_in, kev
FROM image_findings WHERE scan_id IN (sqlc.slice(scan_ids)) AND vuln_id IN (sqlc.slice(vuln_ids));

-- name: ListScanFindingsAtLeast :many
-- The findings of some images at or above a severity, worst first: what
-- alerts.
SELECT scan_id, severity, vuln_id, package, version, pkg_type, fixed_in, kev
FROM image_findings WHERE scan_id IN (sqlc.slice(scan_ids)) AND severity >= +sqlc.arg(min_severity)
ORDER BY scan_id, severity DESC, vuln_id;

-- name: ListScanFixesAtLeast :many
-- The fixable findings of some images at or above a severity: what fixes
-- the worst once ignores hide some.
SELECT scan_id, severity, vuln_id, package, pkg_type
FROM image_findings WHERE scan_id IN (sqlc.slice(scan_ids)) AND severity >= +sqlc.arg(min_severity) AND fixed_in != '';

-- name: PreviousServiceScan :one
-- The scanned image of a service's release before release number: what
-- a new live image's vulnerabilities are new against.
SELECT r.image_scan_id FROM releases r
JOIN image_scans i ON i.id = r.image_scan_id AND i.matched_at IS NOT NULL
WHERE r.service_id = +sqlc.arg(service_id) AND r.number < +sqlc.arg(number)
ORDER BY r.number DESC LIMIT 1;

-- name: UpsertVulnIgnore :one
-- Ignoring the same vulnerability in the same place again replaces the
-- reason and expiry.
INSERT INTO vuln_ignores (org_id, project_id, service_id, vuln_id, package, reason, expires_at, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (org_id, project_id, service_id, vuln_id, package) DO UPDATE SET
    reason = excluded.reason, expires_at = excluded.expires_at, created_by = excluded.created_by, created_at = excluded.created_at
RETURNING *;

-- name: ListActiveVulnIgnores :many
-- The org's ignores in force, read with its findings (usually none).
SELECT * FROM vuln_ignores
WHERE org_id = +sqlc.arg(org_id) AND (expires_at IS NULL OR expires_at > +sqlc.arg(now));

-- name: ListVulnIgnores :many
-- Every ignore of the org, those expired after cutoff too, with where
-- they apply by name, newest first: the Ignores page.
SELECT g.*, coalesce(p.name, '') AS project, coalesce(e.id, 0) AS environment_id, coalesce(e.name, '') AS environment,
       coalesce(s.name, '') AS service
FROM vuln_ignores g
LEFT JOIN projects p ON p.id = g.project_id
LEFT JOIN services s ON s.id = g.service_id
LEFT JOIN environments e ON e.id = s.environment_id
WHERE g.org_id = +sqlc.arg(org_id) AND (g.expires_at IS NULL OR g.expires_at > +sqlc.arg(cutoff))
ORDER BY g.id DESC LIMIT 1000;

-- name: GetVulnIgnore :one
SELECT * FROM vuln_ignores WHERE id = +sqlc.arg(id);

-- name: DeleteVulnIgnore :exec
DELETE FROM vuln_ignores WHERE id = +sqlc.arg(id);

-- name: PruneVulnIgnores :execrows
-- Ignores expired before cutoff, deleted when an ignore is made.
DELETE FROM vuln_ignores WHERE expires_at < +sqlc.arg(cutoff);

-- name: GetOrgVulnAlerts :one
SELECT vuln_alerts FROM orgs WHERE id = +sqlc.arg(id);

-- name: SetOrgVulnAlerts :exec
UPDATE orgs SET vuln_alerts = ? WHERE id = +sqlc.arg(id);

-- Phase 3: base image hints and database imports.

-- name: SetReleaseBase :exec
UPDATE releases SET base_image = ?, base_digest = ? WHERE id = +sqlc.arg(id);

-- name: ListLiveBaseImages :many
-- The base images live images were built on, each once, with an org that
-- runs one (whose registry credential asks for it): the daily check of
-- whether their tags moved.
SELECT i.base_image, CAST(min(i.org_id) AS INTEGER) AS org_id FROM image_scans i
WHERE i.base_image != '' AND i.base_digest != ''
  AND EXISTS (SELECT 1 FROM releases r WHERE r.image_scan_id = i.id AND r.status = 'active')
GROUP BY i.base_image LIMIT +sqlc.arg(max_rows);

-- name: SetBaseImageDigest :many
-- What a base image's tag points at now: the images built on an older
-- digest have a newer base a rebuild picks up, and an image deployed by
-- that tag has a newer image to update to. Returns the scans it updated,
-- for their pages to learn of it. base_image != '' lets SQLite use the
-- partial index image_scans_base.
UPDATE image_scans SET base_moved = CAST(base_digest != sqlc.arg(digest) AS INTEGER), base_latest = sqlc.arg(digest),
  base_checked_at = sqlc.arg(checked_at)
WHERE base_image = +sqlc.arg(base_image) AND base_image != '' AND base_digest != ''
RETURNING *;

-- name: GetTagsChecked :one
-- When the daily tag check last finished (0 for never): at start-up, a
-- day after it the next is due.
SELECT tags_checked_at FROM platform_settings WHERE id = 1;

-- name: SetTagsChecked :exec
-- Records a finished daily tag check. Not an operator's change: an
-- existing row keeps its updated_by and updated_at.
INSERT INTO platform_settings (id, tags_checked_at, updated_by, updated_at) VALUES (1, ?, 'system', ?)
ON CONFLICT (id) DO UPDATE SET tags_checked_at = excluded.tags_checked_at;

-- name: GetTagDigest :one
-- What a tag pointed at at its last daily check since checked_after: the
-- digest a new template database starts on. SetBaseImageDigest writes
-- every row of a tag at once, so any one of them says it.
SELECT base_latest FROM image_scans
WHERE base_image = +sqlc.arg(base_image) AND base_image != '' AND base_latest != ''
  AND base_checked_at > +sqlc.arg(checked_after)
LIMIT 1;

-- name: GetScanDatabaseImport :one
SELECT scan_db_import FROM platform_settings WHERE id = 1;

-- name: SetScanDatabaseImport :exec
INSERT INTO platform_settings (id, scan_db_import, updated_by, updated_at) VALUES (1, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET scan_db_import = excluded.scan_db_import, updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- name: SetServerScanImport :exec
-- The database archive a builder imported (its sha256).
UPDATE servers SET scan_db_import = ? WHERE id = +sqlc.arg(id);

-- name: SetServiceAutoUpdate :exec
-- Turns a service's automatic image updates on (1) or off (0); applies
-- at once, never staged.
UPDATE services SET auto_update = ? WHERE id = +sqlc.arg(id);

-- name: ListImageAutoUpdates :many
-- The live releases of services with auto_update on whose image is its
-- tag's own (deployed by tag, not built on it) while the tag points at a
-- newer image: what the daily check updates. CROSS JOIN keeps services
-- outermost, read through services_auto_update: the few that update on
-- their own, not every live release.
SELECT r.service_id, r.image_ref, i.base_image, i.base_latest
FROM services s
CROSS JOIN releases r ON r.service_id = s.id AND r.status = 'active'
JOIN image_scans i ON i.id = r.image_scan_id
WHERE s.auto_update = 1 AND s.archived_at IS NULL AND s.stopped_at IS NULL
  AND i.base_image != '' AND i.base_moved = 1 AND i.base_latest != '' AND i.base_digest = i.image_key;
