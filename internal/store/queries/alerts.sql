-- name: OpenAlert :one
INSERT INTO alerts (key, kind, severity, title, message, project, environment, service, server, opened_at, updated_at, project_id, service_id, command, target, org_id, muted_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UnmuteAlert :exec
-- An alert held by a maintenance window, sent once the window ended.
UPDATE alerts SET muted_by = NULL WHERE id = +?;

-- name: UpdateAlert :exec
UPDATE alerts SET severity = ?, title = ?, message = ?, project = ?, command = ?, target = ?, updated_at = ? WHERE id = +?;

-- name: ResolveAlert :exec
UPDATE alerts SET resolved_at = sqlc.arg(at), updated_at = sqlc.arg(at) WHERE id = +sqlc.arg(id) AND resolved_at IS NULL;

-- name: GetAlert :one
SELECT * FROM alerts WHERE id = +?;

-- name: ListOpenAlerts :many
-- Read once when the daemon starts; the alert engine keeps them in memory.
SELECT * FROM alerts WHERE resolved_at IS NULL ORDER BY id DESC;

-- name: ListAlerts :many
-- An org's alerts (org 0: the platform's), open or not, newest first;
-- page with before_id.
SELECT * FROM alerts
WHERE coalesce(org_id, 0) = +CAST(sqlc.arg(org) AS INTEGER) AND id < +sqlc.arg(before_id)
ORDER BY id DESC LIMIT +sqlc.arg(max_rows);

-- name: AlertProjectOrg :one
-- The org of the project an alert is about.
SELECT org_id FROM projects WHERE id = +?;

-- name: AlertServiceOrg :one
-- The org of the service an alert is about.
SELECT projects.org_id FROM services
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE services.id = +?;

-- name: PruneResolvedAlerts :execrows
DELETE FROM alerts WHERE resolved_at < +?;

-- name: CreateAlertChannel :one
INSERT INTO alert_channels (org_id, name, kind, target, config, min_severity, renotify_minutes, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAlertChannels :many
-- An org's channels (org 0: the platform's), by name.
SELECT * FROM alert_channels WHERE coalesce(org_id, 0) = +CAST(sqlc.arg(org) AS INTEGER) ORDER BY name;

-- name: GetAlertChannel :one
SELECT * FROM alert_channels WHERE coalesce(org_id, 0) = +CAST(sqlc.arg(org) AS INTEGER) AND name = +sqlc.arg(name);

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

-- name: ListNoticedChannels :many
-- The channels told about an alert, whichever org they are in now.
SELECT alert_channels.* FROM alert_notices
JOIN alert_channels ON alert_channels.id = alert_notices.channel_id
WHERE alert_notices.alert_id = +?;

-- name: DeleteAlertNotices :exec
DELETE FROM alert_notices WHERE alert_id = +?;

-- name: ListDueReminders :many
-- Open alerts a channel was told about longer than its renotify interval
-- ago. Notices exist only for open alerts, a handful at a time.
SELECT alert_notices.alert_id, sqlc.embed(alert_channels) FROM alert_notices
JOIN alert_channels ON alert_channels.id = alert_notices.channel_id
WHERE alert_channels.renotify_minutes > 0
  AND alert_notices.notified_at + alert_channels.renotify_minutes * 60 <= CAST(sqlc.arg(now) AS INTEGER);

-- name: ListFailedLatestReleases :many
-- Services whose newest release failed: what failed-deploy alerts should
-- be open after the alert engine missed events, with their cause. One pass over services
-- (CROSS JOIN keeps SQLite from scanning releases instead), each newest
-- release found through (service_id, number), walking back past canceled
-- ones: a cancel is no outcome (ADR 0086).
SELECT projects.id AS project_id, services.id AS service_id,
  projects.name AS project, environments.name AS environment, services.name AS service,
  releases.number, releases.error,
  -- Its diagnosis's cause, when a rule matched (ADR 0071).
  CAST(coalesce((SELECT d.summary FROM diagnoses d WHERE d.release_id = releases.id AND d.rule <> '' ORDER BY d.id DESC LIMIT 1), '') AS TEXT) AS cause
FROM services
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
CROSS JOIN releases ON releases.service_id = services.id
  AND releases.number = (SELECT newest.number FROM releases newest WHERE newest.service_id = services.id
    AND newest.error_code <> 'deploy_canceled' ORDER BY newest.number DESC LIMIT 1)
WHERE releases.status = 'failed' AND services.archived_at IS NULL;

-- Rename only open alerts of this project; closed alerts keep their snapshots.
-- name: RenameProjectAlerts :many
UPDATE alerts SET project = sqlc.arg(name),
    title = replace(title, sqlc.arg(old_name) || '/' || environment || '/' || service, sqlc.arg(name) || '/' || environment || '/' || service),
    command = replace(command, '-p ' || sqlc.arg(old_name) || ' ', '-p ' || sqlc.arg(name) || ' ')
WHERE project_id = +sqlc.arg(project_id) AND resolved_at IS NULL
RETURNING *;

-- name: ListAlertChannelCopies :many
-- Platform channels still to be copied into an org (0070_org_alerts.sql).
SELECT sqlc.embed(alert_channels), alert_channel_copies.org_id AS copy_org_id FROM alert_channel_copies
JOIN alert_channels ON alert_channels.id = alert_channel_copies.channel_id;

-- name: DeleteAlertChannelCopy :exec
DELETE FROM alert_channel_copies WHERE channel_id = +? AND org_id = +?;

-- ListEmailChannels is every email channel, the platform's and every
-- org's, to move their settings onto the platform's SMTP server.
-- name: ListEmailChannels :many
SELECT * FROM alert_channels WHERE kind = 'email' ORDER BY id;

-- name: SetAlertChannelConfig :exec
UPDATE alert_channels SET config = ?, target = ? WHERE id = +?;
