-- name: CreateCLILogin :one
INSERT INTO cli_logins (device_hash, user_code, client_name, hostname, os, ip, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetCLILoginByUserCode :one
SELECT * FROM cli_logins WHERE user_code = ?;

-- name: GetCLILoginByDeviceHash :one
SELECT * FROM cli_logins WHERE device_hash = ?;

-- name: CountPendingCLILogins :one
SELECT count(*) FROM cli_logins WHERE status = 'pending' AND expires_at > ?;

-- name: DecideCLILogin :execrows
UPDATE cli_logins SET status = ?, user_id = ?
WHERE id = ? AND status = 'pending' AND expires_at > ?;

-- name: DeleteCLILogin :execrows
DELETE FROM cli_logins WHERE id = ? AND status = ?;

-- name: DeleteExpiredCLILogins :exec
DELETE FROM cli_logins WHERE expires_at < ?;

-- name: CreateCLILoginToken :one
INSERT INTO tokens (name, hash, scope, created_by, expires_at, origin)
VALUES (?, ?, ?, ?, ?, 'cli_login')
RETURNING *;
