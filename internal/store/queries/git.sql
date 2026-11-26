-- name: GetGitConnectionByService :one
SELECT * FROM git_connections WHERE service_id = +?;

-- name: GetGitConnectionByHook :one
SELECT * FROM git_connections WHERE hook_id = +?;

-- name: GetAppGitConnections :many
-- The App connections of one repository, which an App push delivery is
-- routed to. Preview environments' connections are left out: only their
-- pull request's events deploy them (ADR 0056).
SELECT * FROM git_connections
WHERE repo_id = +? AND installation_id = +?
  AND NOT EXISTS (SELECT 1 FROM services JOIN previews ON previews.environment_id = services.environment_id
                  WHERE services.id = git_connections.service_id);

-- name: AdoptAppGitConnections :execrows
-- Moves a repository's App connections whose installation Dock no longer
-- knows (the App was replaced, or uninstalled and installed again) to
-- the installation an App delivery for that repository came from, when
-- that installation is assigned to the connection's org. The delivery
-- proves the installation reaches the repository; the org check keeps
-- one org from taking another's. Stale installations are found through
-- git_connections_app; the table has a row per connected service.
UPDATE git_connections SET installation_id = sqlc.arg(installation_id)
WHERE git_connections.repo_id = +sqlc.arg(repo_id)
  AND git_connections.installation_id IN (SELECT DISTINCT g.installation_id FROM git_connections g
                          WHERE g.installation_id > 0
                            AND g.installation_id NOT IN (SELECT installation_id FROM github_installations))
  AND git_connections.service_id IN (SELECT services.id FROM services
                     JOIN environments ON environments.id = services.environment_id
                     JOIN projects ON projects.id = environments.project_id
                     WHERE projects.org_id = (SELECT org_id FROM github_installations
                                              WHERE github_installations.installation_id = +sqlc.arg(installation_id)));

-- name: CreateGitConnection :one
INSERT INTO git_connections (service_id, hook_id, repo, branch, dir, secret, token, post_status, installation_id, repo_id, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateGitConnection :one
-- Changes what a connection deploys. hook_id and secret change only when
-- the secret is rotated (the caller passes the old ones otherwise);
-- installation_id and repo_id are 0 for a webhook connection.
UPDATE git_connections
SET repo = ?, branch = ?, dir = ?, token = ?, post_status = ?, hook_id = ?, secret = ?, installation_id = ?, repo_id = ?
WHERE id = +?
RETURNING *;

-- name: DeleteGitConnection :exec
DELETE FROM git_connections WHERE service_id = +?;

-- name: SetGitPending :exec
-- Queues a push, replacing any older one that hasn't started.
UPDATE git_connections
SET pending_sha = ?, pending_message = ?, pending_author = ?, pending_by = ?, pending_force = ?, pending_approved = ?
WHERE id = +?;

-- name: ClearGitPending :execrows
-- Clears the queued push if it is still sha (a newer one may have
-- replaced it meanwhile).
UPDATE git_connections
SET pending_sha = '', pending_message = '', pending_author = '', pending_by = '', pending_force = 0, pending_approved = 0
WHERE id = +sqlc.arg(id) AND pending_sha = CAST(sqlc.arg(pending_sha) AS TEXT);

-- name: SetGitLastEvent :one
UPDATE git_connections SET last_event = ?, last_event_at = ? WHERE id = +?
RETURNING *;

-- name: ListGitPendingServices :many
SELECT service_id FROM git_connections WHERE pending_sha > '';

-- name: AddGitDelivery :exec
-- Records a delivery; a unique violation means its ID or its body hash
-- (sha256 of hook ID and body) was seen within the replay window.
INSERT INTO git_deliveries (delivery_id, body_hash, created_at) VALUES (?, ?, ?);

-- name: PruneGitDeliveries :exec
DELETE FROM git_deliveries WHERE created_at < +?;

-- name: SetReleaseCommit :exec
UPDATE releases SET commit_sha = ?, commit_message = ?, commit_author = ? WHERE id = +?;

-- name: ListOrgGitServices :many
-- The org's services that deploy from GitHub, with their repository and
-- directory, for the repository and directory pickers' "used by".
-- Archived ones and preview environments' are left out. Reached through
-- projects' (org_id, name) index, then each table's own keys.
SELECT git_connections.repo_id, git_connections.repo, git_connections.dir,
       projects.name AS project, environments.name AS environment, services.name AS service
FROM projects
JOIN environments ON environments.project_id = projects.id
JOIN services ON services.environment_id = environments.id
JOIN git_connections ON git_connections.service_id = services.id
WHERE projects.org_id = +? AND projects.archived_at IS NULL
  AND environments.archived_at IS NULL AND services.archived_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM previews WHERE previews.environment_id = environments.id)
ORDER BY projects.name, environments.name, services.name;
