-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CreateUser :one
INSERT INTO users (email, name, password_hash, operator)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = +?;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = +?;

-- name: CreateSession :exec
INSERT INTO sessions (user_id, hash, expires_at) VALUES (?, ?, ?);

-- name: GetSessionUser :one
SELECT sqlc.embed(users), sessions.expires_at AS session_expires_at
FROM sessions JOIN users ON users.id = sessions.user_id
WHERE sessions.hash = +?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE hash = +?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at < +?;

-- name: CreateToken :one
INSERT INTO tokens (name, hash, scope, org_id, operator, project_id, environment_id, created_by, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- GetTokenByHash also returns the user a `dock login` token acts for and
-- a manual token's org, in the same query, since authentication checks
-- them on every request.
-- name: GetTokenByHash :one
SELECT sqlc.embed(tokens), users.email AS user_email, users.operator AS user_operator,
    users.disabled_at AS user_disabled_at, orgs.slug AS org_slug
FROM tokens LEFT JOIN users ON users.id = tokens.user_id LEFT JOIN orgs ON orgs.id = tokens.org_id
WHERE tokens.hash = +?;

-- name: GetToken :one
SELECT * FROM tokens WHERE id = +?;

-- ListOrgTokens is an org's manual tokens, then its members' `dock
-- login` tokens (which have no org), each through an index.
-- name: ListOrgTokens :many
SELECT * FROM tokens WHERE tokens.org_id = +sqlc.arg(org_id)
UNION ALL
SELECT tokens.* FROM org_members JOIN tokens ON tokens.user_id = org_members.user_id
WHERE org_members.org_id = +sqlc.arg(org_id) AND tokens.origin = 'cli_login';

-- name: TouchToken :exec
UPDATE tokens SET last_used_at = ? WHERE id = +?;

-- name: RevokeToken :exec
UPDATE tokens SET revoked_at = ? WHERE id = +? AND revoked_at IS NULL;

-- RevokeUserLoginTokens revokes the `dock login` tokens acting for a user,
-- except token keep (0: none).
-- name: RevokeUserLoginTokens :execrows
UPDATE tokens SET revoked_at = +sqlc.arg(now)
WHERE user_id = +sqlc.arg(user_id) AND origin = 'cli_login' AND revoked_at IS NULL AND id != +sqlc.arg(keep);

-- name: SetUserDisabled :exec
UPDATE users SET disabled_at = ? WHERE id = +?;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = ? WHERE id = +?;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = +?;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = +?;

-- ListOrgMembers is an org's users with their role, live sessions and
-- usable tokens. Tokens record their creator by name, which for users is
-- their email.
-- name: ListOrgMembers :many
SELECT sqlc.embed(users), org_members.role,
    CAST((SELECT count(*) FROM sessions WHERE sessions.user_id = users.id AND sessions.expires_at > +sqlc.arg(now)) AS INTEGER) AS sessions,
    CAST((SELECT count(*) FROM tokens WHERE tokens.created_by = users.email AND tokens.revoked_at IS NULL
        AND (tokens.expires_at IS NULL OR tokens.expires_at > +sqlc.arg(now))) AS INTEGER) AS tokens
FROM org_members JOIN users ON users.id = org_members.user_id
WHERE org_members.org_id = +sqlc.arg(org_id)
ORDER BY org_members.user_id;
