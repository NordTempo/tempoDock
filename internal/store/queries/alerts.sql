-- name: OpenAlert :one
INSERT INTO alerts (key, kind, severity, title, message, project, environment, service, server, opened_at, updated_at, project_id, service_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateAlert :exec
UPDATE alerts SET severity = ?, title = ?, message = ?, project = ?, updated_at = ? WHERE id = +?;

-- name: ResolveAlert :exec
UPDATE alerts SET resolved_at = sqlc.arg(at), updated_at = sqlc.arg(at) WHERE id = +sqlc.arg(id) AND resolved_at IS NULL;

-- name: GetAlert :one
SELECT * FROM alerts WHERE id = +?;

-- name: ListOpenAlerts :many
-- Read once when the daemon starts; the alert engine keeps them in memory.
SELECT * FROM alerts WHERE resolved_at IS NULL ORDER BY id DESC;

-- name: ListAlerts :many
-- Every alert, open or not, newest first; page with before_id.
SELECT * FROM alerts WHERE id < +sqlc.arg(before_id) ORDER BY id DESC LIMIT +sqlc.arg(max_rows);

-- name: PruneResolvedAlerts :execrows
DELETE FROM alerts WHERE resolved_at < +?;

-- name: CreateAlertChannel :one
INSERT INTO alert_channels (name, kind, target, config, min_severity, renotify_minutes, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAlertChannels :many
SELECT * FROM alert_channels ORDER BY name;

-- name: GetAlertChannel :one
SELECT * FROM alert_channels WHERE name = +?;

-- name: GetAlertChannelByID :one
SELECT * FROM alert_channels WHERE id = +?;

-- name: DeleteAlertChannel :exec
DELETE FROM alert_channels WHERE id = +?;

-- name: SetAlertChannelSent :exec
UPDATE alert_channels SET last_sent_at = ?, failures = 0 WHERE id = +?;

-- name: SetAlertChannelFailed :exec
UPDATE alert_channels SET last_error = ?, last_error_at = ?, failures = failures + 1 WHERE id = +?;

-- name: AddAlertNotice :exec
INSERT OR REPLACE INTO alert_notices (alert_id, channel_id, notified_at) VALUES (?, ?, ?);

-- name: ListAlertNotices :many
-- The channels told about an alert.
SELECT channel_id FROM alert_notices WHERE alert_id = +?;

-- name: DeleteAlertNotices :exec
DELETE FROM alert_notices WHERE alert_id = +?;

-- name: ListDueReminders :many
-- Open alerts a channel was told about longer than its renotify interval
-- ago. Notices exist only for open alerts, a handful at a time.
SELECT alert_notices.alert_id, alert_notices.channel_id FROM alert_notices
JOIN alert_channels ON alert_channels.id = alert_notices.channel_id
WHERE alert_channels.renotify_minutes > 0
  AND alert_notices.notified_at + alert_channels.renotify_minutes * 60 <= CAST(sqlc.arg(now) AS INTEGER);

-- name: ListFailedLatestReleases :many
-- Services whose newest release failed: what failed-deploy alerts should
-- be open after the alert engine missed events. One pass over services
-- (CROSS JOIN keeps SQLite from scanning releases instead), each newest
-- release found through (service_id, number).
SELECT projects.id AS project_id, services.id AS service_id,
  projects.name AS project, environments.name AS environment, services.name AS service,
  releases.number, releases.error
FROM services
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
CROSS JOIN releases ON releases.service_id = services.id
  AND releases.number = (SELECT max(newest.number) FROM releases newest WHERE newest.service_id = services.id)
WHERE releases.status = 'failed' AND services.archived_at IS NULL;

-- Rename only open alerts of this project; closed alerts keep their snapshots.
-- name: RenameProjectAlerts :many
UPDATE alerts SET project = sqlc.arg(name),
    title = replace(title, sqlc.arg(old_name) || '/' || environment || '/' || service, sqlc.arg(name) || '/' || environment || '/' || service)
WHERE project_id = +sqlc.arg(project_id) AND resolved_at IS NULL
RETURNING *;
