-- name: GetAnalyticsSettings :one
SELECT * FROM analytics_settings WHERE id = 1;

-- CreateAnalyticsSettings makes the row with a new token and the
-- analytics server, unless it exists.
-- name: CreateAnalyticsSettings :exec
INSERT INTO analytics_settings (id, token, server_id, updated_by, updated_at) VALUES (1, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: SetAnalyticsServer :exec
UPDATE analytics_settings SET server_id = ?, updated_by = ?, updated_at = ? WHERE id = 1;

-- name: SetAnalyticsEnabled :exec
UPDATE analytics_settings SET enabled = 1, updated_by = ?, updated_at = ? WHERE id = 1;

-- ListAnalyticsSalts returns the salts of day and later, oldest first.
-- name: ListAnalyticsSalts :many
SELECT day, salt FROM analytics_salts WHERE day >= +sqlc.arg(day) ORDER BY day;

-- name: AddAnalyticsSalt :exec
INSERT INTO analytics_salts (day, salt) VALUES (?, ?) ON CONFLICT (day) DO NOTHING;

-- name: DeleteAnalyticsSaltsBefore :exec
DELETE FROM analytics_salts WHERE day < +sqlc.arg(day);
