-- Users, sessions, tokens and links. Parameters compared to indexed
-- columns are written `= +?` and limits `LIMIT +?` (docs/performance.md,
-- the re-prepare pitfall).

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CountActiveAdmins :one
-- Admins who can still sign in; users is small (people, not data).
SELECT count(*) FROM users WHERE role = 'admin' AND disabled_at IS NULL;

-- name: CreateUser :one
INSERT INTO users (email, name, role, password_hash, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = +?;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = +?;

-- name: ListUsers :many
SELECT * FROM users ORDER BY id;

-- name: UpdateUser :one
UPDATE users SET name = ?, role = ?, disabled_at = ? WHERE id = +?
RETURNING *;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = ? WHERE id = +?;

-- name: DeleteUser :exec
-- Cascades to the user's sessions, tokens, reset links and saved queries.
DELETE FROM users WHERE id = +?;

-- name: CreateSession :exec
INSERT INTO sessions (user_id, secret_hash, created_at, expires_at) VALUES (?, ?, ?, ?);

-- name: GetSessionUser :one
-- Authenticates a session cookie: the session and its user in one query.
SELECT sqlc.embed(users), sessions.id AS session_id, sessions.expires_at AS session_expires_at
FROM sessions JOIN users ON users.id = sessions.user_id
WHERE sessions.secret_hash = +?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE secret_hash = +?;

-- name: DeleteSessionByID :exec
DELETE FROM sessions WHERE id = +?;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = +?;

-- name: DeleteExpiredSessions :exec
-- Run when a session starts; nothing sweeps sessions on a timer.
DELETE FROM sessions WHERE expires_at < +?;

-- name: CreateToken :one
INSERT INTO tokens (name, prefix, secret_hash, user_id, data_production, data_non_production, data_sensitive, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: AddTokenConnection :exec
INSERT INTO token_connections (token_id, connection_id) VALUES (?, ?);

-- name: GetTokenAuth :one
-- Authenticates a bearer token: the token, its user and its connection
-- limit (comma-separated IDs, '' for none) in one query.
SELECT sqlc.embed(tokens), users.email AS user_email, users.role AS user_role, users.disabled_at AS user_disabled_at,
    CAST(coalesce((SELECT group_concat(connection_id) FROM token_connections WHERE token_connections.token_id = tokens.id), '') AS TEXT) AS connection_ids
FROM tokens JOIN users ON users.id = tokens.user_id
WHERE tokens.secret_hash = +?;

-- name: GetToken :one
-- One token with its user's email and its connection limit by name.
SELECT sqlc.embed(tokens), users.email AS user_email,
    CAST(coalesce((SELECT group_concat(connections.name) FROM token_connections
        JOIN connections ON connections.id = token_connections.connection_id
        WHERE token_connections.token_id = tokens.id), '') AS TEXT) AS connection_names
FROM tokens JOIN users ON users.id = tokens.user_id
WHERE tokens.id = +?;

-- name: ListTokens :many
-- Every token, for admins; like GetToken.
SELECT sqlc.embed(tokens), users.email AS user_email,
    CAST(coalesce((SELECT group_concat(connections.name) FROM token_connections
        JOIN connections ON connections.id = token_connections.connection_id
        WHERE token_connections.token_id = tokens.id), '') AS TEXT) AS connection_names
FROM tokens JOIN users ON users.id = tokens.user_id
ORDER BY tokens.id;

-- name: ListUserTokens :many
-- One user's tokens; like GetToken.
SELECT sqlc.embed(tokens), users.email AS user_email,
    CAST(coalesce((SELECT group_concat(connections.name) FROM token_connections
        JOIN connections ON connections.id = token_connections.connection_id
        WHERE token_connections.token_id = tokens.id), '') AS TEXT) AS connection_names
FROM tokens JOIN users ON users.id = tokens.user_id
WHERE tokens.user_id = +?
ORDER BY tokens.id;

-- name: SetTokenData :exec
-- Sets a token's own data access; NULLs follow the tokens rule again.
UPDATE tokens SET
    data_production = sqlc.narg(data_production),
    data_non_production = sqlc.narg(data_non_production),
    data_sensitive = sqlc.narg(data_sensitive)
WHERE id = +sqlc.arg(id);

-- name: RevokeToken :execrows
UPDATE tokens SET revoked_at = ? WHERE id = +? AND revoked_at IS NULL;

-- name: CreateLink :one
INSERT INTO links (kind, email, role, user_id, secret_hash, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetLinkByHash :one
SELECT * FROM links WHERE secret_hash = +?;

-- name: UseLink :execrows
-- Spends a link; no row when it was used or expired meanwhile, so two
-- concurrent uses can't both win.
UPDATE links SET used_at = sqlc.arg(now)
WHERE id = +sqlc.arg(id) AND used_at IS NULL AND expires_at > sqlc.arg(now);

-- name: ListOpenInvites :many
-- Invites not used yet, newest first; expired ones too, so admins see
-- them lapse and can delete them.
SELECT * FROM links WHERE kind = 'invite' AND used_at IS NULL ORDER BY id DESC;

-- name: DeleteInvite :execrows
DELETE FROM links WHERE id = +? AND kind = 'invite' AND used_at IS NULL;

-- name: DeleteUserResetLinks :exec
-- A new reset link replaces the user's open ones.
DELETE FROM links WHERE user_id = +? AND used_at IS NULL;
