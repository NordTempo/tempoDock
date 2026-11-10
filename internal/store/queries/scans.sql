-- Image vulnerability scanning (ADR 0074).

-- name: ListUnscannedReleases :many
-- Live releases whose image has no scan yet, with what picking or
-- making one needs. Through the releases_unscanned partial index.
SELECT r.id, r.service_id, r.image_ref, r.image_id, r.image_digest, r.pinned_ref, r.built_on_server_id,
       e.kind AS env_kind, e.server_id, p.org_id
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
INSERT INTO image_scans (image_key, pull_ref, local_ref, built_on_server_id, org_id, production, priority, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: RaiseImageScanPriority :exec
-- A scan another, more important release uses now: production first.
UPDATE image_scans SET production = max(production, sqlc.arg(production)), priority = min(priority, sqlc.arg(priority))
WHERE id = +sqlc.arg(id);

-- name: NextImageScan :one
-- The next scan to run: queued, due, and one a builder there is may
-- scan (production ones only with a builder that accepts production).
SELECT * FROM image_scans
WHERE state = 'queued' AND (next_attempt_at IS NULL OR next_attempt_at <= +sqlc.arg(now))
  AND (production = 0 OR +sqlc.arg(production_builder) = 1)
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
       s.id AS service_id, s.name AS service, e.id AS environment_id, e.name AS environment, e.kind AS env_kind, p.id AS project_id, p.name AS project
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
SELECT r.id AS release_id, r.number, r.status, r.image_ref, s.id AS service_id, s.name AS service,
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
       s.name AS service, e.id AS environment_id, e.name AS environment, e.kind AS env_kind, p.id AS project_id, p.name AS project, r.number
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
-- Scans by state, for the console. One row per image in use.
SELECT state, count(*) AS n FROM image_scans GROUP BY state;

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
