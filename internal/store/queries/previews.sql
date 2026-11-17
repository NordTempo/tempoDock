-- Preview environments per pull request (ADR 0056).

-- name: GetEnvironmentPreviews :one
-- The numbers of previews cloned from an environment (ADR 0077).
SELECT * FROM environment_previews WHERE environment_id = +?;

-- name: UpsertEnvironmentPreviews :exec
INSERT INTO environment_previews (environment_id, max_previews, sleep_after_minutes, copy_shared, updated_by, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (environment_id) DO UPDATE SET
    max_previews = excluded.max_previews, sleep_after_minutes = excluded.sleep_after_minutes,
    copy_shared = excluded.copy_shared, updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- name: GetServicePreviews :one
-- A service's preview settings (ADR 0077).
SELECT * FROM service_previews WHERE service_id = +?;

-- name: UpsertServicePreviews :exec
INSERT INTO service_previews (service_id, build, allow_forks, copy_variables, data, data_forks, updated_by, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (service_id) DO UPDATE SET
    build = excluded.build, allow_forks = excluded.allow_forks, copy_variables = excluded.copy_variables,
    data = excluded.data, data_forks = excluded.data_forks, updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- name: StopServicePreviewBuilds :exec
-- A disconnected service builds no more pull requests.
UPDATE service_previews SET build = 0, allow_forks = 0 WHERE service_id = +?;

-- name: ListEnvServicePreviews :many
-- The preview settings of an environment's live services, with their
-- names, for making a preview from it.
SELECT service_previews.*, services.name
FROM services JOIN service_previews ON service_previews.service_id = services.id
WHERE services.environment_id = +? AND services.archived_at IS NULL
ORDER BY services.name;

-- name: ListProjectServicePreviews :many
-- Every live service of a project's live environments with preview
-- settings, with its environment and GitHub source (empty without one),
-- for the project's Previews summary and for the rule that one
-- repository and branch previews from one environment.
SELECT service_previews.*, services.name AS service, environments.id AS environment_id, environments.name AS environment,
       CAST(coalesce(git_connections.repo, '') AS TEXT) AS repo, CAST(coalesce(git_connections.branch, '') AS TEXT) AS branch,
       CAST(coalesce(git_connections.dir, '') AS TEXT) AS dir, CAST(coalesce(git_connections.repo_id, 0) AS INTEGER) AS repo_id
FROM environments
JOIN services ON services.environment_id = environments.id
JOIN service_previews ON service_previews.service_id = services.id
LEFT JOIN git_connections ON git_connections.service_id = services.id
WHERE environments.project_id = +? AND environments.archived_at IS NULL AND services.archived_at IS NULL
ORDER BY environments.name, services.name;

-- name: CountOpenBasePreviews :one
-- The open previews cloned from an environment, which its max_previews
-- caps.
SELECT count(*) FROM previews WHERE base_environment_id = +? AND closed_at IS NULL;

-- name: ListAppPreviewConnections :many
-- The App connections of one repository whose service builds pull
-- requests, or in an open preview (so its pull request closing still
-- archives it after previews were turned off), which an App pull_request
-- delivery is routed to.
SELECT git_connections.* FROM git_connections
JOIN services ON services.id = git_connections.service_id
WHERE git_connections.repo_id = +? AND git_connections.installation_id = +? AND services.archived_at IS NULL
  AND (EXISTS (SELECT 1 FROM service_previews WHERE service_previews.service_id = services.id AND service_previews.build)
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
-- Canceled releases are passed over (ADR 0086).
SELECT services.environment_id, services.name,
       CAST(coalesce((SELECT releases.status FROM releases WHERE releases.service_id = services.id
                      AND releases.error_code <> 'deploy_canceled'
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

-- name: StartPreviewData :exec
-- A preview database's copy starts (ADR 0092): at the preview's creation,
-- or again on a refresh from its base.
INSERT INTO preview_data (service_id, preview_id, from_service_id, mode, status, message, started_at)
VALUES (?, ?, ?, ?, 'copying', ?, ?)
ON CONFLICT (service_id) DO UPDATE SET
    mode = excluded.mode, status = 'copying', message = excluded.message, bytes = 0, masked_tables = 0, masked_columns = 0,
    started_at = excluded.started_at, finished_at = NULL;

-- name: SetPreviewDataMessage :exec
-- The step a copy in progress is at.
UPDATE preview_data SET message = ? WHERE service_id = +? AND status = 'copying';

-- name: FinishPreviewData :exec
-- A preview database's copy ends: copied, with its numbers, or empty with
-- the reason.
UPDATE preview_data SET status = ?, message = ?, bytes = ?, masked_tables = ?, masked_columns = ?, finished_at = ?
WHERE service_id = +?;

-- name: ListProjectPreviewData :many
-- The copies of a project's open previews, with the preview database's
-- and its base service's names, for the previews list.
SELECT preview_data.*, services.name AS service, base.name AS from_service, environments.name AS from_environment
FROM previews
JOIN preview_data ON preview_data.preview_id = previews.id
JOIN services ON services.id = preview_data.service_id
JOIN services AS base ON base.id = preview_data.from_service_id
JOIN environments ON environments.id = base.environment_id
WHERE previews.project_id = +? AND previews.closed_at IS NULL
ORDER BY preview_data.preview_id, services.name;

-- name: ListCopyingPreviewData :many
-- Copies a restart cut short, to end them at the next start.
SELECT preview_data.*, previews.environment_id, previews.closed_at FROM preview_data
JOIN previews ON previews.id = preview_data.preview_id
WHERE preview_data.status = 'copying';
