-- The platform console's Organizations page (ADR 0062): every org with
-- its size and use, in a fixed number of queries however many orgs.

-- name: ListOperatorOrgs :many
-- Every org that isn't deleted, with its own members (not operators who
-- joined for a while) and its live projects and services, by slug. One
-- pass over orgs; each count reads through an index.
SELECT sqlc.embed(orgs),
    CAST((SELECT count(*) FROM org_members WHERE org_members.org_id = orgs.id AND org_members.expires_at IS NULL) AS INTEGER) AS members,
    CAST((SELECT count(*) FROM projects WHERE projects.org_id = orgs.id AND projects.archived_at IS NULL) AS INTEGER) AS projects,
    CAST((SELECT count(*) FROM projects
        JOIN environments ON environments.project_id = projects.id
        JOIN services ON services.environment_id = environments.id
        WHERE projects.org_id = orgs.id AND services.archived_at IS NULL) AS INTEGER) AS services
FROM orgs WHERE orgs.deleted_at IS NULL ORDER BY orgs.slug;

-- name: ListOrgAdmins :many
-- Every org's own admins, each org's first admin (its owner) first.
SELECT org_members.org_id, users.email, users.name FROM org_members
JOIN users ON users.id = org_members.user_id
WHERE org_members.role = 'admin' AND org_members.expires_at IS NULL
ORDER BY org_members.org_id, org_members.created_at;

-- name: ListServiceOrgs :many
-- Every live service's project and org, to add up use per org.
SELECT services.id AS service_id, projects.id AS project_id, projects.org_id FROM projects
JOIN environments ON environments.project_id = projects.id
JOIN services ON services.environment_id = environments.id
WHERE services.archived_at IS NULL;

-- name: ListServiceCPUBetween :many
-- Each service's average CPU (hundredths of a percent of one core) over
-- the 10-minute points in [from_at, to_at): what it used a while ago.
SELECT service_id, CAST(avg(cpu) AS INTEGER) AS cpu FROM service_stats
WHERE res = 600 AND at >= +sqlc.arg(from_at) AND at < +sqlc.arg(to_at)
GROUP BY service_id;

-- name: SumOrgBackups :many
-- Each org's backups: the size of every live service's newest backup
-- that worked.
SELECT projects.org_id, CAST(sum(backups.size_bytes) AS INTEGER) AS bytes FROM projects
JOIN environments ON environments.project_id = projects.id
JOIN services ON services.environment_id = environments.id
JOIN backups ON backups.id = (
    SELECT max(newest.id) FROM backups newest
    WHERE newest.service_id = services.id AND newest.operation = 'backup' AND newest.status = 'succeeded')
WHERE services.archived_at IS NULL
GROUP BY projects.org_id;

-- name: SumOrgTraffic :many
-- Each org's bytes served since since_at, from the hourly traffic points.
SELECT projects.org_id, CAST(sum(traffic_stats.bytes) AS INTEGER) AS bytes FROM traffic_stats
JOIN services ON services.id = traffic_stats.service_id
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE traffic_stats.res = 3600 AND traffic_stats.at >= +sqlc.arg(since_at)
GROUP BY projects.org_id;

-- name: SuspendOrg :one
UPDATE orgs SET suspended_at = ?, suspended_by = ? WHERE id = +? RETURNING *;

-- name: MarkOrgDeleted :exec
UPDATE orgs SET deleted_at = ? WHERE id = +?;

-- name: AddExpiringOrgMember :exec
INSERT INTO org_members (org_id, user_id, role, expires_at) VALUES (?, ?, 'admin', ?);

-- name: ListExpiredOrgMembers :many
-- Operators' memberships that ended, for the retention pass to remove.
SELECT org_members.org_id, org_members.user_id, users.email FROM org_members
JOIN users ON users.id = org_members.user_id
WHERE org_members.expires_at <= unixepoch();

-- name: ListOrgLiveServices :many
-- An org's live services, for an operator stopping them all.
SELECT projects.name AS project, environments.name AS environment, services.name AS service, services.stopped_at
FROM projects
JOIN environments ON environments.project_id = projects.id
JOIN services ON services.environment_id = environments.id
WHERE projects.org_id = +? AND projects.archived_at IS NULL AND services.archived_at IS NULL
ORDER BY projects.name, environments.name, services.name;

-- name: DeleteOrgMember :exec
DELETE FROM org_members WHERE org_id = +? AND user_id = +?;
