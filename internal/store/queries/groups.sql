-- Groups and grants (ADR 0080, phase 4).

-- name: CreateGroup :one
INSERT INTO groups (org_id, name, seeded) VALUES (?, ?, ?) RETURNING *;

-- name: GetGroup :one
SELECT * FROM groups WHERE id = +? AND org_id = +?;

-- name: GetGroupByName :one
SELECT * FROM groups WHERE org_id = +? AND name = +?;

-- GetSeededGroup is one of the org's four seeded groups, through
-- groups_seeded.
-- name: GetSeededGroup :one
SELECT * FROM groups WHERE org_id = +? AND seeded = +?;

-- name: CountGroups :one
SELECT count(*) FROM groups WHERE org_id = +?;

-- name: ListGroups :many
SELECT * FROM groups WHERE org_id = +? ORDER BY seeded IS NULL, name;

-- name: RenameGroup :exec
UPDATE groups SET name = ? WHERE id = +? AND org_id = +?;

-- name: DeleteGroup :exec
DELETE FROM groups WHERE id = +? AND org_id = +?;

-- name: AddGroupMember :exec
INSERT INTO group_members (group_id, user_id) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: RemoveGroupMember :execrows
DELETE FROM group_members WHERE group_id = +? AND user_id = +?;

-- RemoveFromRoleGroups takes a person out of the org's Admins,
-- Developers and Viewers, before a role change puts them in one.
-- name: RemoveFromRoleGroups :exec
DELETE FROM group_members WHERE user_id = +sqlc.arg(user_id)
  AND group_id IN (SELECT id FROM groups WHERE org_id = +sqlc.arg(org_id) AND seeded IN ('admins', 'developers', 'viewers'));

-- RemoveFromOrgGroups takes a person out of every group of an org and
-- ends their one-off grants there: they left it.
-- name: RemoveFromOrgGroups :exec
DELETE FROM group_members WHERE user_id = +sqlc.arg(user_id)
  AND group_id IN (SELECT id FROM groups WHERE org_id = +sqlc.arg(org_id));

-- name: DeleteUserGrants :exec
DELETE FROM grants WHERE user_id = +sqlc.arg(user_id) AND org_id = +sqlc.arg(org_id);

-- ListGroupMembers is every member of the org's groups, with who they are.
-- name: ListGroupMembers :many
SELECT group_members.group_id, users.id AS user_id, users.email, users.name FROM groups
JOIN group_members ON group_members.group_id = groups.id
JOIN users ON users.id = group_members.user_id
WHERE groups.org_id = +?
ORDER BY users.email;

-- name: CreateGrant :one
INSERT INTO grants (org_id, group_id, user_id, token_id, role, role_id, project_id, environment_id, kinds, expires_at, via, granted_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetGrant :one
SELECT grants.*, coalesce(roles.name, grants.role) AS role_name FROM grants
LEFT JOIN roles ON roles.id = grants.role_id
WHERE grants.id = +sqlc.arg(id) AND grants.org_id = +sqlc.arg(org_id);

-- name: DeleteGrant :exec
DELETE FROM grants WHERE id = +? AND org_id = +?;

-- ListOrgGrants is every grant of the org, groups' and one-offs, with the
-- names of their places, for the groups and members pages. Expired ones
-- are left out (they end when read; no timer removes them).
-- name: ListOrgGrants :many
SELECT grants.*, projects.name AS project_name, environments.name AS environment_name,
       roles.name AS custom_role, roles.parts AS custom_parts FROM grants
LEFT JOIN projects ON projects.id = grants.project_id
LEFT JOIN environments ON environments.id = grants.environment_id
LEFT JOIN roles ON roles.id = grants.role_id
WHERE grants.org_id = +sqlc.arg(org_id) AND (grants.expires_at IS NULL OR grants.expires_at > +sqlc.arg(now))
ORDER BY grants.id;

-- ListUserGrants is what a person holds in an org: their one-off grants
-- and their groups' grants, through grants_user, group_members_user and
-- grants_group, with a custom role's parts by its key. Authentication
-- runs it on every request.
-- name: ListUserGrants :many
SELECT grants.role, roles.parts, grants.project_id, grants.environment_id, grants.kinds FROM grants
LEFT JOIN roles ON roles.id = grants.role_id
WHERE grants.user_id = +sqlc.arg(user_id) AND grants.org_id = +sqlc.arg(org_id)
  AND (grants.expires_at IS NULL OR grants.expires_at > +sqlc.arg(now))
UNION ALL
SELECT grants.role, roles.parts, grants.project_id, grants.environment_id, grants.kinds FROM group_members
JOIN grants ON grants.group_id = group_members.group_id
LEFT JOIN roles ON roles.id = grants.role_id
WHERE group_members.user_id = +sqlc.arg(user_id) AND grants.org_id = +sqlc.arg(org_id)
  AND (grants.expires_at IS NULL OR grants.expires_at > +sqlc.arg(now));

-- CountFullAccess is how many people hold Full access on the whole org
-- without an end, through a group or their own grant: the org keeps one.
-- name: CountFullAccess :one
SELECT count(DISTINCT u) FROM (
  SELECT grants.user_id AS u FROM grants
  WHERE grants.org_id = +sqlc.arg(org_id) AND grants.role = 'full' AND grants.user_id IS NOT NULL
    AND grants.project_id IS NULL AND grants.environment_id IS NULL AND grants.kinds = 7 AND grants.expires_at IS NULL
  UNION ALL
  SELECT group_members.user_id FROM grants JOIN group_members ON group_members.group_id = grants.group_id
  WHERE grants.org_id = +sqlc.arg(org_id) AND grants.role = 'full'
    AND grants.project_id IS NULL AND grants.environment_id IS NULL AND grants.kinds = 7 AND grants.expires_at IS NULL
);

-- name: SetEnvironmentPreview :exec
UPDATE environments SET preview = 1 WHERE id = +?;

-- ListOrgsWithoutGroups is the live orgs not seeded with groups yet: older
-- than ADR 0080's phase 4, seeded at start.
-- name: ListOrgsWithoutGroups :many
SELECT orgs.id FROM orgs
WHERE orgs.deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM groups WHERE groups.org_id = orgs.id)
ORDER BY orgs.id;

-- ListOrgMemberRoles is an org's members with their role, and when an
-- operator's membership ends, for seeding its groups.
-- name: ListOrgMemberRoles :many
SELECT user_id, role, expires_at FROM org_members WHERE org_id = +?;

-- ListOrgEnvironments is every live environment of the org's live
-- projects, by project and name, for a person's effective access: one
-- query for the whole org.
-- name: ListOrgEnvironments :many
SELECT environments.id, environments.name, environments.kind, environments.preview,
    projects.id AS project_id, projects.name AS project_name FROM projects
JOIN environments ON environments.project_id = projects.id
WHERE projects.org_id = +? AND projects.archived_at IS NULL AND environments.archived_at IS NULL
ORDER BY projects.name, environments.name;

-- name: CreateRole :one
INSERT INTO roles (org_id, name, description, parts, created_by) VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetRoleByName :one
SELECT * FROM roles WHERE org_id = +? AND name = ? COLLATE NOCASE;

-- name: ListRoles :many
SELECT * FROM roles WHERE org_id = +? ORDER BY name;

-- name: UpdateRole :exec
UPDATE roles SET name = ?, description = ?, parts = ? WHERE id = +? AND org_id = +?;

-- name: DeleteRole :exec
DELETE FROM roles WHERE id = +? AND org_id = +?;

-- RoleUses is who holds a custom role: the groups and people its grants
-- go to, for the role editor and refusing to remove one in use.
-- name: RoleUses :many
SELECT grants.role_id, groups.name AS group_name, users.email AS user_email FROM grants
LEFT JOIN groups ON groups.id = grants.group_id
LEFT JOIN users ON users.id = grants.user_id
WHERE grants.org_id = +sqlc.arg(org_id) AND grants.role_id IS NOT NULL;

-- ListTokenGrants is what an org agent holds: its own grants and its
-- groups', through grants_token, group_agents and grants_group. Every
-- request of an org token runs it.
-- name: ListTokenGrants :many
SELECT grants.role, roles.parts, grants.project_id, grants.environment_id, grants.kinds FROM grants
LEFT JOIN roles ON roles.id = grants.role_id
WHERE grants.token_id = +sqlc.arg(token_id)
  AND (grants.expires_at IS NULL OR grants.expires_at > +sqlc.arg(now))
UNION ALL
SELECT grants.role, roles.parts, grants.project_id, grants.environment_id, grants.kinds FROM group_agents
JOIN grants ON grants.group_id = group_agents.group_id
LEFT JOIN roles ON roles.id = grants.role_id
WHERE group_agents.token_id = +sqlc.arg(token_id)
  AND (grants.expires_at IS NULL OR grants.expires_at > +sqlc.arg(now));

-- ListTokensToGrant is the org tokens whose scope and limit aren't grants
-- yet, for the core to turn them into grants at start.
-- name: ListTokensToGrant :many
SELECT * FROM tokens
WHERE granted = 0 AND origin = 'manual' AND platform IS NULL AND org_id IS NOT NULL AND revoked_at IS NULL;

-- name: SetTokenGranted :exec
UPDATE tokens SET granted = 1 WHERE id = +?;

-- name: SetTokenOwnGrants :exec
UPDATE tokens SET own_grants = 1 WHERE id = +?;

-- name: AddGroupAgent :exec
INSERT INTO group_agents (group_id, token_id) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: RemoveGroupAgent :execrows
DELETE FROM group_agents WHERE group_id = +? AND token_id = +?;

-- ListGroupAgents is every org agent in the org's groups, by name.
-- name: ListGroupAgents :many
SELECT group_agents.group_id, tokens.id AS token_id, tokens.name FROM groups
JOIN group_agents ON group_agents.group_id = groups.id
JOIN tokens ON tokens.id = group_agents.token_id
WHERE groups.org_id = +? AND tokens.revoked_at IS NULL
ORDER BY tokens.name;
