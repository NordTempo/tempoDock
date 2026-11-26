-- GitHub Apps (ADR 0031, 0105): each the platform's (org_id NULL) or one
-- org's, with the installations orgs deploy from.

-- name: GetGitHubApp :one
SELECT * FROM github_apps WHERE id = +?;

-- name: GetGitHubAppBySlug :one
-- An App by its slug on GitHub, which is unique there.
SELECT * FROM github_apps WHERE slug = +?;

-- name: GetGitHubAppByHook :one
-- The App a delivery to /hooks/github/<hook_id> is for.
SELECT * FROM github_apps WHERE hook_id = +?;

-- name: ListGitHubApps :many
-- Every App with its org's slug (empty for the platform's): the platform's
-- first, then by org, oldest first.
SELECT sqlc.embed(a), CAST(coalesce(o.slug, '') AS TEXT) AS org_slug
FROM github_apps a LEFT JOIN orgs o ON o.id = a.org_id
ORDER BY a.org_id IS NOT NULL, a.org_id, a.id;

-- name: ListOrgGitHubApps :many
-- The Apps an org may deploy through, its own and the platform's, in no
-- order (a handful; core sorts them).
SELECT * FROM github_apps WHERE org_id = +? OR org_id IS NULL;

-- name: CreateGitHubApp :one
-- Stores an App registered through the manifest flow. Its sealed values
-- are bound to its hook_id, which is known before the insert.
INSERT INTO github_apps (org_id, app_id, hook_id, slug, name, owner, owner_id, html_url, private_key, webhook_secret,
                         client_id, client_secret, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: SetGitHubAppClient :execrows
-- Gives an App its OAuth client (ADR 0066), for Apps made before Dock
-- kept it. No row affected: there is no such App.
UPDATE github_apps SET client_id = ?, client_secret = ? WHERE id = +?;

-- name: SetGitHubAppSealed :exec
-- Rewrites an App's sealed values, for resealGitHubApps.
UPDATE github_apps SET private_key = ?, webhook_secret = ?, client_secret = ? WHERE id = +?;

-- name: DeleteGitHubApp :exec
-- Forgets an App; its installations go with it.
DELETE FROM github_apps WHERE id = +?;

-- name: ListGitHubInstallations :many
-- Every assigned installation with its org's slug, for the console.
SELECT i.installation_id, i.app_id, i.org_id, i.account, i.assigned_by, i.assigned_at, o.slug AS org_slug
FROM github_installations i JOIN orgs o ON o.id = i.org_id
ORDER BY i.installation_id;

-- name: ListOrgGitHubInstallations :many
SELECT * FROM github_installations WHERE org_id = +? ORDER BY installation_id;

-- name: GetGitHubInstallation :one
SELECT * FROM github_installations WHERE installation_id = +?;

-- name: SetGitHubInstallation :exec
-- Assigns an installation to an org, moving it if it had another.
INSERT INTO github_installations (installation_id, app_id, org_id, account, assigned_by, assigned_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (installation_id) DO UPDATE SET
    app_id = excluded.app_id, org_id = excluded.org_id, account = excluded.account,
    assigned_by = excluded.assigned_by, assigned_at = excluded.assigned_at;

-- name: ClaimGitHubInstallation :execrows
-- Assigns an installation to an org unless another org has it; an
-- assignment to the same org is refreshed. No row affected means another
-- org has it. The check is part of the write, so no race gets past it.
INSERT INTO github_installations (installation_id, app_id, org_id, account, assigned_by, assigned_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (installation_id) DO UPDATE SET
    account = excluded.account, assigned_by = excluded.assigned_by, assigned_at = excluded.assigned_at
WHERE github_installations.org_id = excluded.org_id AND github_installations.app_id = excluded.app_id;

-- name: DeleteGitHubInstallation :exec
DELETE FROM github_installations WHERE installation_id = +?;

-- name: DeleteAppGitHubInstallation :execrows
-- Forgets an installation of one App, for that App's uninstall event: a
-- delivery to one App can't unassign another's installation.
DELETE FROM github_installations WHERE installation_id = +? AND app_id = +?;

-- name: CountInstallationConnections :many
-- Connections per installation, for the console.
SELECT installation_id, COUNT(*) AS connections FROM git_connections
WHERE installation_id > 0 GROUP BY installation_id;
