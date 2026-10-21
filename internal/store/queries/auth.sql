-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CreateUser :one
INSERT INTO users (email, name, password_hash, role)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ?;

-- name: ListUsers :many
SELECT * FROM users ORDER BY id;

-- name: CreateSession :exec
INSERT INTO sessions (user_id, hash, expires_at) VALUES (?, ?, ?);

-- name: GetSessionUser :one
SELECT sqlc.embed(users), sessions.expires_at AS session_expires_at
FROM sessions JOIN users ON users.id = sessions.user_id
WHERE sessions.hash = ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE hash = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at < ?;

-- name: CreateToken :one
INSERT INTO tokens (name, hash, scope, project_id, environment_id, created_by, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetTokenByHash :one
SELECT * FROM tokens WHERE hash = ?;

-- name: GetToken :one
SELECT * FROM tokens WHERE id = ?;

-- name: ListTokens :many
SELECT * FROM tokens ORDER BY id;

-- name: TouchToken :exec
UPDATE tokens SET last_used_at = ? WHERE id = ?;

-- name: RevokeToken :exec
UPDATE tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL;
