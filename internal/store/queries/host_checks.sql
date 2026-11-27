-- name: ListHostChecks :many
-- Every server's last host check (ADR 0109), read once on start to know
-- when each is due and what each has open.
SELECT * FROM host_checks;

-- name: GetHostChecks :one
SELECT * FROM host_checks WHERE server_id = +?;

-- name: SetHostChecks :exec
INSERT INTO host_checks (server_id, checked_at, results, error) VALUES (?, ?, ?, ?)
ON CONFLICT (server_id) DO UPDATE SET checked_at = excluded.checked_at, results = excluded.results, error = excluded.error;

-- name: ListAllHostCheckDismissals :many
-- Every dismissed host check finding, read once on start.
SELECT * FROM host_check_dismissals;

-- name: ListHostCheckDismissals :many
-- A server's dismissed findings, by key.
SELECT * FROM host_check_dismissals WHERE server_id = +? ORDER BY finding;

-- name: DismissHostFinding :exec
INSERT INTO host_check_dismissals (server_id, finding, reason, dismissed_by, dismissed_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (server_id, finding) DO UPDATE SET reason = excluded.reason, dismissed_by = excluded.dismissed_by,
    dismissed_at = excluded.dismissed_at;

-- name: RestoreHostFinding :execrows
DELETE FROM host_check_dismissals WHERE server_id = +? AND finding = +?;
