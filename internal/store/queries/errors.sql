-- Error issues (ADR 0062, "One place for errors"). The statistics tick
-- writes them in one transaction a minute; the errors page reads them in
-- a fixed number of queries: the issues, the org's counts for trends, and
-- the facets.

-- name: GetErrorIssueByFingerprint :one
SELECT * FROM error_issues WHERE service_id = +? AND fingerprint = +?;

-- name: CreateErrorIssue :one
INSERT INTO error_issues (org_id, project_id, environment_id, service_id, fingerprint, source, title, culprit, sample,
                          count, first_seen, last_seen, first_release, last_release)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateErrorIssueSeen :exec
-- An issue seen again: the tick has read it in the same transaction and
-- passes what it becomes (a resolved one reopens as regressed).
UPDATE error_issues SET count = ?, title = ?, culprit = ?, sample = ?, last_seen = ?, last_release = ?,
                        resolved_at = ?, resolved_by = ?, regressed_at = ?
WHERE id = +?;

-- name: CountServiceErrorIssues :one
-- A service's issues, against the cap on how many it may have.
SELECT count(*) FROM error_issues WHERE service_id = +?;

-- name: AddErrorCount :exec
INSERT INTO error_counts (issue_id, hour, count) VALUES (?, ?, ?)
ON CONFLICT (issue_id, hour) DO UPDATE SET count = count + excluded.count;

-- name: GetErrorTarget :one
-- Where a service's errors go: its environment, project and org (with
-- their names, for alerts), the server it runs on, its active release's
-- number (0 for none) and whether it, its environment or its project is
-- archived.
SELECT services.id, services.environment_id, environments.project_id, projects.org_id, environments.server_id,
       projects.name AS project, environments.name AS environment, services.name AS service,
       CAST(services.archived_at IS NOT NULL OR environments.archived_at IS NOT NULL OR projects.archived_at IS NOT NULL AS INTEGER) AS archived,
       CAST(coalesce((SELECT number FROM releases WHERE releases.service_id = services.id AND releases.status = 'active'), 0) AS INTEGER) AS active_release
FROM services
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE services.id = +?;

-- name: ListOrgErrorIssues :many
-- An org's issues in live services, last seen first, without samples.
-- environment '' is every environment; state 0 every issue, 1 open ones,
-- 2 resolved ones.
SELECT i.id, i.fingerprint, i.source, i.title, i.culprit, i.count, i.first_seen, i.last_seen, i.first_release,
       i.last_release, i.resolved_at, i.resolved_by, i.regressed_at, i.project_id, i.environment_id, i.service_id,
       p.name AS project, e.name AS environment, s.name AS service,
       CAST(coalesce(r.number, 0) AS INTEGER) AS active_release
FROM error_issues i
JOIN services s ON s.id = i.service_id
JOIN environments e ON e.id = i.environment_id
JOIN projects p ON p.id = i.project_id
LEFT JOIN releases r ON r.service_id = i.service_id AND r.status = 'active'
WHERE i.org_id = +sqlc.arg(org_id)
  AND (CAST(sqlc.arg(environment) AS TEXT) = '' OR e.name = CAST(sqlc.arg(environment) AS TEXT))
  AND (CAST(sqlc.arg(state) AS INTEGER) = 0 OR (i.resolved_at IS NULL) = (CAST(sqlc.arg(state) AS INTEGER) = 1))
  AND s.archived_at IS NULL AND e.archived_at IS NULL AND p.archived_at IS NULL
ORDER BY i.last_seen DESC
LIMIT +sqlc.arg(max_rows);

-- name: ListProjectErrorIssues :many
-- ListOrgErrorIssues for one project.
SELECT i.id, i.fingerprint, i.source, i.title, i.culprit, i.count, i.first_seen, i.last_seen, i.first_release,
       i.last_release, i.resolved_at, i.resolved_by, i.regressed_at, i.project_id, i.environment_id, i.service_id,
       p.name AS project, e.name AS environment, s.name AS service,
       CAST(coalesce(r.number, 0) AS INTEGER) AS active_release
FROM error_issues i
JOIN services s ON s.id = i.service_id
JOIN environments e ON e.id = i.environment_id
JOIN projects p ON p.id = i.project_id
LEFT JOIN releases r ON r.service_id = i.service_id AND r.status = 'active'
WHERE i.project_id = +sqlc.arg(project_id)
  AND (CAST(sqlc.arg(environment) AS TEXT) = '' OR e.name = CAST(sqlc.arg(environment) AS TEXT))
  AND (CAST(sqlc.arg(state) AS INTEGER) = 0 OR (i.resolved_at IS NULL) = (CAST(sqlc.arg(state) AS INTEGER) = 1))
  AND s.archived_at IS NULL AND e.archived_at IS NULL AND p.archived_at IS NULL
ORDER BY i.last_seen DESC
LIMIT +sqlc.arg(max_rows);

-- name: ListServiceErrorIssues :many
-- ListOrgErrorIssues for one service.
SELECT i.id, i.fingerprint, i.source, i.title, i.culprit, i.count, i.first_seen, i.last_seen, i.first_release,
       i.last_release, i.resolved_at, i.resolved_by, i.regressed_at, i.project_id, i.environment_id, i.service_id,
       p.name AS project, e.name AS environment, s.name AS service,
       CAST(coalesce(r.number, 0) AS INTEGER) AS active_release
FROM error_issues i
JOIN services s ON s.id = i.service_id
JOIN environments e ON e.id = i.environment_id
JOIN projects p ON p.id = i.project_id
LEFT JOIN releases r ON r.service_id = i.service_id AND r.status = 'active'
WHERE i.service_id = +sqlc.arg(service_id)
  AND (CAST(sqlc.arg(state) AS INTEGER) = 0 OR (i.resolved_at IS NULL) = (CAST(sqlc.arg(state) AS INTEGER) = 1))
  AND s.archived_at IS NULL AND e.archived_at IS NULL AND p.archived_at IS NULL
ORDER BY i.last_seen DESC
LIMIT +sqlc.arg(max_rows);

-- name: ListOrgErrorCounts :many
-- The hourly counts since an hour of an org's issues last seen since a
-- time: every trend the errors page shows, in one range.
SELECT c.issue_id, c.hour, c.count
FROM error_issues i
JOIN error_counts c ON c.issue_id = i.id AND c.hour >= +sqlc.arg(since_hour)
WHERE i.org_id = +sqlc.arg(org_id) AND i.last_seen >= sqlc.arg(seen_since);

-- name: ListErrorFacets :many
-- An org's open issues per live service: how many, how many are new or
-- regressed (first seen since new_since, or in the service's active
-- release), and their errors since since_hour. environment '' is every
-- environment.
SELECT i.service_id, i.project_id, i.environment_id, p.name AS project, e.name AS environment, s.name AS service,
       count(*) AS issues,
       CAST(sum(i.regressed_at IS NOT NULL OR i.first_seen >= sqlc.arg(new_since)
                OR (i.first_release != 0 AND i.first_release = coalesce(r.number, 0))) AS INTEGER) AS new_issues,
       CAST(sum((SELECT coalesce(sum(c.count), 0) FROM error_counts c
                 WHERE c.issue_id = i.id AND c.hour >= +sqlc.arg(since_hour))) AS INTEGER) AS events
FROM error_issues i
JOIN services s ON s.id = i.service_id
JOIN environments e ON e.id = i.environment_id
JOIN projects p ON p.id = i.project_id
LEFT JOIN releases r ON r.service_id = i.service_id AND r.status = 'active'
WHERE i.org_id = +sqlc.arg(org_id) AND i.resolved_at IS NULL
  AND (CAST(sqlc.arg(environment) AS TEXT) = '' OR e.name = CAST(sqlc.arg(environment) AS TEXT))
  AND s.archived_at IS NULL AND e.archived_at IS NULL AND p.archived_at IS NULL
GROUP BY i.service_id;

-- name: GetErrorIssue :one
-- One issue with its sample, where it is and its service's active
-- release number.
SELECT sqlc.embed(error_issues), p.name AS project, e.name AS environment, s.name AS service,
       CAST(coalesce(r.number, 0) AS INTEGER) AS active_release,
       CAST(s.archived_at IS NOT NULL OR e.archived_at IS NOT NULL OR p.archived_at IS NOT NULL AS INTEGER) AS archived
FROM error_issues
JOIN services s ON s.id = error_issues.service_id
JOIN environments e ON e.id = error_issues.environment_id
JOIN projects p ON p.id = error_issues.project_id
LEFT JOIN releases r ON r.service_id = error_issues.service_id AND r.status = 'active'
WHERE error_issues.id = +?;

-- name: ListErrorIssueCounts :many
-- One issue's hourly counts since an hour, oldest first.
SELECT hour, count FROM error_counts WHERE issue_id = +? AND hour >= +? ORDER BY hour;

-- name: ResolveErrorIssue :execrows
UPDATE error_issues SET resolved_at = ?, resolved_by = ?, regressed_at = NULL
WHERE id = +? AND resolved_at IS NULL;

-- name: AutoResolveErrorIssues :execrows
-- Open issues without an occurrence since cutoff resolve by themselves.
UPDATE error_issues SET resolved_at = sqlc.arg(now), resolved_by = '', regressed_at = NULL
WHERE resolved_at IS NULL AND last_seen < sqlc.arg(cutoff);

-- name: PruneErrorIssues :execrows
-- Resolved issues not seen since cutoff go, with their counts.
DELETE FROM error_issues WHERE resolved_at IS NOT NULL AND last_seen < ?;

-- name: PruneErrorCounts :execrows
DELETE FROM error_counts WHERE hour < +?;

-- name: CountReleaseNewErrors :one
-- The issues first seen in a release of a service.
SELECT count(*) FROM error_issues WHERE service_id = +? AND first_release = +?;

-- name: ListFollowContainers :many
-- The active release of each service on a server that has a container,
-- with what its lines are labelled with (as ListDrainContainers): what
-- the log followers (errors and drains) follow when the daemon starts
-- or the server reconnects.
SELECT sqlc.embed(releases), services.name AS service, environments.id AS environment_id,
       environments.name AS environment, environments.server_id, projects.name AS project
FROM environments
JOIN services ON services.environment_id = environments.id
JOIN releases ON releases.service_id = services.id AND releases.status = 'active'
JOIN projects ON projects.id = environments.project_id
WHERE environments.server_id = +? AND releases.container_id != '';
