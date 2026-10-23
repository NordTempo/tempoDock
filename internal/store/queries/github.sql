-- name: GetGitHubApp :one
-- The install's GitHub App; no row until one is registered.
SELECT * FROM github_app WHERE id = 1;

-- name: SetGitHubApp :exec
-- Stores the App registered through the manifest flow, replacing any
-- earlier one.
INSERT INTO github_app (id, app_id, hook_id, slug, name, owner, html_url, private_key, webhook_secret, created_by, created_at)
VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    app_id = excluded.app_id, hook_id = excluded.hook_id, slug = excluded.slug, name = excluded.name, owner = excluded.owner,
    html_url = excluded.html_url, private_key = excluded.private_key, webhook_secret = excluded.webhook_secret,
    created_by = excluded.created_by, created_at = excluded.created_at;

-- name: DeleteGitHubApp :exec
DELETE FROM github_app;

-- name: ListGitHubInstallations :many
-- Every assigned installation with its org's slug, for the console.
SELECT i.installation_id, i.org_id, i.account, i.assigned_by, i.assigned_at, o.slug AS org_slug
FROM github_installations i JOIN orgs o ON o.id = i.org_id
ORDER BY i.installation_id;

-- name: ListOrgGitHubInstallations :many
SELECT * FROM github_installations WHERE org_id = +? ORDER BY installation_id;

-- name: GetGitHubInstallation :one
SELECT * FROM github_installations WHERE installation_id = +?;

-- name: SetGitHubInstallation :exec
-- Assigns an installation to an org, moving it if it had another.
INSERT INTO github_installations (installation_id, org_id, account, assigned_by, assigned_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (installation_id) DO UPDATE SET
    org_id = excluded.org_id, account = excluded.account, assigned_by = excluded.assigned_by, assigned_at = excluded.assigned_at;

-- name: DeleteGitHubInstallation :exec
DELETE FROM github_installations WHERE installation_id = +?;

-- name: DeleteGitHubInstallations :exec
DELETE FROM github_installations;

-- name: CountInstallationConnections :many
-- Connections per installation, for the console.
SELECT installation_id, COUNT(*) AS connections FROM git_connections
WHERE installation_id > 0 GROUP BY installation_id;
