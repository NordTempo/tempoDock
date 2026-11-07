-- Preview environments per pull request (ADR 0056).

-- name: GetPreviewSettings :one
SELECT * FROM preview_settings WHERE project_id = +?;

-- name: UpsertPreviewSettings :exec
INSERT INTO preview_settings (project_id, base_environment_id, max_previews, copy_variables, allow_forks, seed_from_snapshot, updated_by, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (project_id) DO UPDATE SET
    base_environment_id = excluded.base_environment_id, max_previews = excluded.max_previews,
    copy_variables = excluded.copy_variables, allow_forks = excluded.allow_forks,
    seed_from_snapshot = excluded.seed_from_snapshot,
    updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- name: DeletePreviewSettings :execrows
DELETE FROM preview_settings WHERE project_id = +?;

-- name: ListAppPreviewConnections :many
-- The App connections of one repository in a base environment previews
-- are made from, or in an open preview (so its pull request closing
-- still archives it after previews were turned off), which an App
-- pull_request delivery is routed to.
SELECT git_connections.* FROM git_connections
JOIN services ON services.id = git_connections.service_id
WHERE git_connections.repo_id = +? AND git_connections.installation_id = +? AND services.archived_at IS NULL
  AND (EXISTS (SELECT 1 FROM preview_settings WHERE preview_settings.base_environment_id = services.environment_id)
       OR EXISTS (SELECT 1 FROM previews WHERE previews.environment_id = services.environment_id AND previews.closed_at IS NULL))
ORDER BY git_connections.id;

-- name: ListEnvGitConnections :many
-- The git connections of an environment's services, archived ones'
-- included (a closed preview's), by service ID.
SELECT git_connections.* FROM git_connections
JOIN services ON services.id = git_connections.service_id
WHERE services.environment_id = +?
ORDER BY git_connections.service_id;

-- name: ListClosedPreviewsLeft :many
-- Closed previews whose environment isn't archived yet: a deploy was
-- running when the pull request closed, or the daemon stopped first.
SELECT previews.* FROM previews
JOIN environments ON environments.id = previews.environment_id
WHERE previews.closed_at IS NOT NULL AND environments.archived_at IS NULL;

-- name: DeleteServiceDomains :many
-- Removes an archived preview service's domains, so their hostnames are
-- free again; returns their IDs.
DELETE FROM domains WHERE service_id = +? RETURNING id;

-- name: GetOpenPreview :one
SELECT * FROM previews
WHERE project_id = +? AND repo = +? AND number = +? AND closed_at IS NULL;

-- name: GetLatestPreview :one
-- The newest preview of a pull request, open or closed.
SELECT * FROM previews
WHERE project_id = +? AND repo = +? AND number = +?
ORDER BY id DESC LIMIT 1;

-- name: GetPreviewByEnvironment :one
SELECT * FROM previews WHERE environment_id = +?;

-- name: GetPreview :one
SELECT * FROM previews WHERE id = +?;

-- name: CountOpenPreviews :one
SELECT count(*) FROM previews WHERE project_id = +? AND closed_at IS NULL;

-- name: ListOpenPreviews :many
-- A project's open previews, newest first.
SELECT * FROM previews WHERE project_id = +? AND closed_at IS NULL ORDER BY id DESC;

-- name: CreatePreview :one
INSERT INTO previews (project_id, environment_id, base_environment_id, repo, number, title, author, html_url, head_branch, head_sha, fork,
                      pr_updated_at, last_event, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdatePreviewHead :exec
-- Records a newer event's pull request: title, head and its updated_at.
UPDATE previews
SET title = ?, author = ?, html_url = ?, head_branch = ?, head_sha = ?, pr_updated_at = ?, updated_at = ?
WHERE id = +?;

-- name: SetPreviewEvent :exec
UPDATE previews SET last_event = ?, updated_at = ? WHERE id = +?;

-- name: SetPreviewComment :exec
UPDATE previews SET comment_id = ? WHERE id = +?;

-- name: ClosePreview :execrows
-- Closes an open preview; 0 rows if it was closed already.
UPDATE previews SET closed_at = ?, pr_updated_at = ?, last_event = ?, updated_at = ?
WHERE id = +? AND closed_at IS NULL;

-- name: ListPreviewServiceStates :many
-- Every live service of a project's open previews (or of one, with
-- environment_id) with its newest release's status, '' for none.
SELECT services.environment_id, services.name,
       CAST(coalesce((SELECT releases.status FROM releases WHERE releases.service_id = services.id
                      ORDER BY releases.number DESC LIMIT 1), '') AS TEXT) AS status
FROM previews
JOIN services ON services.environment_id = previews.environment_id
WHERE previews.project_id = +sqlc.arg(project_id) AND previews.closed_at IS NULL AND services.archived_at IS NULL
  AND (+sqlc.arg(environment_id) = 0 OR previews.environment_id = +sqlc.arg(environment_id))
ORDER BY services.environment_id, services.name;

-- name: ListPreviewDomains :many
-- The serving (not redirecting) domains of a project's open previews (or
-- of one, with environment_id), by service.
SELECT services.environment_id, services.name AS service, domains.hostname
FROM previews
JOIN services ON services.environment_id = previews.environment_id
JOIN domains ON domains.service_id = services.id
WHERE previews.project_id = +sqlc.arg(project_id) AND previews.closed_at IS NULL AND services.archived_at IS NULL
  AND domains.redirect_to = ''
  AND (+sqlc.arg(environment_id) = 0 OR previews.environment_id = +sqlc.arg(environment_id))
ORDER BY services.environment_id, services.name, domains.hostname;

-- name: DeleteEnvironmentGitConnections :exec
-- Removes a closed preview's git connections, which hold a copy of the
-- base's GitHub token (ADR 0056).
DELETE FROM git_connections
WHERE service_id IN (SELECT id FROM services WHERE environment_id = +?);

-- name: DeleteEnvironmentVariables :exec
-- Removes a closed preview's variables, live and staged: its values go
-- with it (ADR 0056).
DELETE FROM variables WHERE environment_id = +?;

-- name: ArchiveEnvironment :exec
-- Archives an environment and renames it, freeing its name.
UPDATE environments SET archived_at = ?, name = ? WHERE id = +? AND archived_at IS NULL;

-- name: ListProjectPreviewEnvironments :many
-- The pull request behind each of a project's preview environments, open
-- or closed, for annotating environments.
SELECT environment_id, repo, number, title, author, html_url, closed_at FROM previews WHERE project_id = +?;

-- name: ListArchivedPreviewVolumes :many
-- The volumes archived previews still have: their removal was cut short
-- by a restart, or their server couldn't be reached. Each row goes once
-- its Docker volume is gone (DeleteRemovedVolume), so this is empty in
-- the steady state. Read once at start: it scans volumes (one row per
-- volume), like ListClosedPreviewsLeft scans previews.
SELECT sqlc.embed(volumes), services.environment_id FROM previews
JOIN environments ON environments.id = previews.environment_id
JOIN services ON services.environment_id = environments.id
JOIN volumes ON volumes.service_id = services.id
WHERE environments.archived_at IS NOT NULL;

-- name: GetLatestReadySnapshot :one
-- A service's newest ready snapshot on a server, which a new preview's
-- seeded service is filled from. Seeks volume_snapshots_service backwards
-- (+server_id keeps the planner off the server's index, which holds every
-- service's snapshots).
SELECT * FROM volume_snapshots
WHERE service_id = +? AND +server_id = ? AND status = 'ready'
ORDER BY id DESC LIMIT 1;
