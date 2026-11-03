-- name: CreateOrg :one
INSERT INTO orgs (slug, name) VALUES (?, ?) RETURNING *;

-- name: GetOrg :one
SELECT * FROM orgs WHERE id = +?;

-- name: OrgSlugTaken :one
SELECT EXISTS (SELECT 1 FROM orgs WHERE slug = +?);

-- name: AddOrgMember :exec
INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, ?);

-- GetMembership is the caller's org and role for the org named by slug.
-- name: GetMembership :one
SELECT sqlc.embed(orgs), org_members.role
FROM org_members JOIN orgs ON orgs.id = org_members.org_id
WHERE org_members.user_id = +sqlc.arg(user_id) AND orgs.slug = +sqlc.arg(slug);

-- ListUserOrgs is every org a user belongs to, with their role in it,
-- oldest first.
-- name: ListUserOrgs :many
SELECT sqlc.embed(orgs), org_members.role
FROM org_members JOIN orgs ON orgs.id = org_members.org_id
WHERE org_members.user_id = +?
ORDER BY org_members.org_id;

-- name: GetMemberRole :one
SELECT role FROM org_members WHERE org_id = +? AND user_id = +?;

-- GetMemberRoleByEmail is the role in an org of the user with an email
-- (tokens record their creator by email).
-- name: GetMemberRoleByEmail :one
SELECT org_members.role FROM org_members JOIN users ON users.id = org_members.user_id
WHERE org_members.org_id = +sqlc.arg(org_id) AND users.email = +sqlc.arg(email);

-- ListOrgServerIDs is the servers an org's environments run on, once per
-- environment (DISTINCT would sort them in a temporary b-tree).
-- name: ListOrgServerIDs :many
SELECT environments.server_id
FROM projects JOIN environments ON environments.project_id = projects.id
WHERE projects.org_id = +?;

-- name: RenameOrg :one
UPDATE orgs SET name = ? WHERE id = +? RETURNING *;

-- name: CountOrgProjects :one
SELECT count(*) FROM projects WHERE org_id = +?;

-- Deleting an org without projects: DeleteOrgMembers, -Invites,
-- -Variables, -DataPolicy, RevokeOrgTokens and DeleteOrg, in one
-- transaction. Audit events stay.
-- name: DeleteOrgMembers :exec
DELETE FROM org_members WHERE org_id = +?;

-- name: DeleteOrgInvites :exec
DELETE FROM org_invites WHERE org_id = +?;

-- name: DeleteOrgVariables :exec
DELETE FROM org_variables WHERE org_id = +?;

-- name: DeleteOrgDataPolicy :exec
DELETE FROM data_policy_rules WHERE org_id = +?;

-- RevokeOrgTokens revokes an org's tokens and detaches them from it, so
-- the org can go; a manual token without an org never authenticates.
-- name: RevokeOrgTokens :exec
UPDATE tokens SET revoked_at = coalesce(revoked_at, +sqlc.arg(now)), org_id = NULL
WHERE org_id = +sqlc.arg(org_id);

-- name: DeleteOrg :exec
DELETE FROM orgs WHERE id = +?;

-- name: SetMemberRole :execrows
UPDATE org_members SET role = ? WHERE org_id = +? AND user_id = +?;

-- name: RemoveOrgMember :execrows
DELETE FROM org_members WHERE org_id = +? AND user_id = +?;

-- name: CountOrgAdmins :one
SELECT count(*) FROM org_members WHERE org_id = +? AND role = 'admin';

-- name: CreateOrgInvite :one
INSERT INTO org_invites (org_id, email, role, hash, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- ListOrgInvites is an org's open invites, newest first; expired ones are
-- listed until revoked so an admin sees they lapsed.
-- name: ListOrgInvites :many
SELECT * FROM org_invites
WHERE org_id = +? AND accepted_at IS NULL AND revoked_at IS NULL
ORDER BY id DESC;

-- name: GetOrgInvite :one
SELECT * FROM org_invites WHERE id = +? AND org_id = +?;

-- GetInviteByHash also returns the invite's org, for the page that shows
-- it before it is accepted.
-- name: GetInviteByHash :one
SELECT sqlc.embed(org_invites), sqlc.embed(orgs)
FROM org_invites JOIN orgs ON orgs.id = org_invites.org_id
WHERE org_invites.hash = +?;

-- name: RevokeOrgInvite :execrows
UPDATE org_invites SET revoked_at = ? WHERE id = +? AND org_id = +? AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: AcceptOrgInvite :execrows
UPDATE org_invites SET accepted_at = ? WHERE id = +? AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: ListOrgVariables :many
SELECT * FROM org_variables WHERE org_id = +? ORDER BY key;

-- name: UpsertOrgVariable :exec
INSERT INTO org_variables (org_id, key, value, updated_at, updated_by) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (org_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by;

-- name: DeleteOrgVariable :execrows
DELETE FROM org_variables WHERE org_id = +? AND key = +?;

-- ListOrgVariableUses is every live or staged variable of an org's live
-- projects, with where it is, for finding ${{org.KEY}} references.
-- name: ListOrgVariableUses :many
SELECT variables.environment_id, variables.service_id, variables.key, variables.value, variables.pending_value,
    projects.name AS project_name, environments.name AS environment_name,
    coalesce(services.name, '') AS service_name
FROM projects
JOIN environments ON environments.project_id = projects.id
JOIN variables ON variables.environment_id = environments.id
LEFT JOIN services ON services.id = variables.service_id
WHERE projects.org_id = +? AND projects.archived_at IS NULL;

-- name: GetOrgBySlug :one
SELECT * FROM orgs WHERE slug = +?;

-- name: ListOrgSlugs :many
-- Every org's slug, for operators choosing one (the GitHub App console).
SELECT slug FROM orgs ORDER BY slug;
