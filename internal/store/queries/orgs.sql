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
