-- name: CountUsers :one
SELECT count(*) FROM users;

-- CreateUser makes an account; email_proven says whether its email is
-- known to reach the person (an invite sign-up's isn't).
-- name: CreateUser :one
INSERT INTO users (email, name, password_hash, platform_role, email_proven)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = +?;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = +?;

-- name: CreateSession :one
INSERT INTO sessions (user_id, hash, expires_at, factor, pending, verified_at, sso_provider_id) VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- GetSessionUser is a session with its user, pending or not: callers
-- check, with the name and label of the provider it signed in through.
-- name: GetSessionUser :one
SELECT sqlc.embed(users), sqlc.embed(sessions), sso_providers.name AS sso_name, sso_providers.label AS sso_label
FROM sessions JOIN users ON users.id = sessions.user_id
LEFT JOIN sso_providers ON sso_providers.id = sessions.sso_provider_id
WHERE sessions.hash = +?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE hash = +?;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at < +?;

-- name: CreateToken :one
INSERT INTO tokens (name, hash, scope, org_id, operator, platform, project_id, environment_id, created_by, user_id, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- GetTokenByHash also returns the user a `dock login` token acts for and
-- a manual token's org in the same query, since authentication checks
-- them on every request.
-- name: GetTokenByHash :one
SELECT sqlc.embed(tokens), users.email AS user_email, users.platform_role AS user_platform_role,
    users.disabled_at AS user_disabled_at, orgs.slug AS org_slug, orgs.suspended_at AS org_suspended_at
FROM tokens LEFT JOIN users ON users.id = tokens.user_id LEFT JOIN orgs ON orgs.id = tokens.org_id
WHERE tokens.hash = +?;

-- GetTokenByIDWithOwner is GetTokenByHash by ID: an approved access
-- request acts as the token that asked, checked as it is now.
-- name: GetTokenByIDWithOwner :one
SELECT sqlc.embed(tokens), users.email AS user_email, users.platform_role AS user_platform_role,
    users.disabled_at AS user_disabled_at, orgs.slug AS org_slug, orgs.suspended_at AS org_suspended_at
FROM tokens LEFT JOIN users ON users.id = tokens.user_id LEFT JOIN orgs ON orgs.id = tokens.org_id
WHERE tokens.id = +?;

-- name: GetToken :one
SELECT * FROM tokens WHERE id = +?;

-- ListOrgTokens is an org's manual tokens, then its members' `dock
-- login` tokens (which have no org) with the member's role, each through
-- an index.
-- name: ListOrgTokens :many
SELECT sqlc.embed(tokens), '' AS member_role FROM tokens WHERE tokens.org_id = +sqlc.arg(org_id)
UNION ALL
SELECT sqlc.embed(tokens), org_members.role FROM org_members JOIN tokens ON tokens.user_id = org_members.user_id
WHERE org_members.org_id = +sqlc.arg(org_id) AND tokens.origin = 'cli_login' AND tokens.platform IS NULL;

-- ListUserTokens is a user's own tokens: their `dock login` tokens and
-- the manual tokens they created in an org, through tokens_user.
-- name: ListUserTokens :many
SELECT * FROM tokens WHERE user_id = +sqlc.arg(user_id)
    AND (origin = 'cli_login' OR org_id = +sqlc.arg(org_id))
ORDER BY id;

-- ListPlatformTokens is the platform's tokens (ADR 0080): the ones
-- operators made, platform logins, and the legacy tokens that are both an
-- org's and the platform's (operator), newest first, through
-- tokens_platform.
-- name: ListPlatformTokens :many
SELECT sqlc.embed(tokens), users.email AS user_email, orgs.slug AS org_slug
FROM tokens LEFT JOIN users ON users.id = tokens.user_id LEFT JOIN orgs ON orgs.id = tokens.org_id
WHERE (tokens.platform IS NOT NULL OR tokens.operator) AND tokens.revoked_at IS NULL
ORDER BY tokens.id DESC;

-- SetTokenOperator ends or sets a token's legacy platform power, for
-- splitting it (core.SplitToken).
-- name: SetTokenOperator :exec
UPDATE tokens SET operator = ? WHERE id = +?;

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

-- name: SetUserName :one
-- Changes a user's display name (PATCH /me).
UPDATE users SET name = ? WHERE id = +? RETURNING *;

-- SetUserPassword also lifts a lock from wrong second factors (ADR 0057).
-- name: SetUserPassword :exec
UPDATE users SET password_hash = ?, factor_failures = 0, factor_failed_at = NULL WHERE id = +?;

-- ProveUserEmail records that the account's email reaches its owner; 1
-- row when it wasn't proven before.
-- name: ProveUserEmail :execrows
UPDATE users SET email_proven = 1 WHERE id = +? AND NOT email_proven;

-- RevokeUserTokens revokes every token a user holds or made by hand.
-- name: RevokeUserTokens :execrows
UPDATE tokens SET revoked_at = +sqlc.arg(now)
WHERE user_id = +sqlc.arg(user_id) AND revoked_at IS NULL;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = +?;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = +?;

-- DeleteOtherSessions ends a user's sessions except one (keep).
-- name: DeleteOtherSessions :exec
DELETE FROM sessions WHERE user_id = +sqlc.arg(user_id) AND id != +sqlc.arg(keep);

-- ListOrgMembers is an org's users with their role, live sessions and
-- usable tokens: the tokens they created (user_id), as their account page
-- lists them. An operator who joined for a while shows until it expires.
-- name: ListOrgMembers :many
SELECT sqlc.embed(users), org_members.role, org_members.expires_at,
    CAST((SELECT count(*) FROM sessions WHERE sessions.user_id = users.id AND sessions.expires_at > +sqlc.arg(now) AND NOT sessions.pending) AS INTEGER) AS sessions,
    CAST((SELECT count(*) FROM tokens WHERE tokens.user_id = users.id AND tokens.revoked_at IS NULL
        AND (tokens.expires_at IS NULL OR tokens.expires_at > +sqlc.arg(now))) AS INTEGER) AS tokens,
    EXISTS (SELECT 1 FROM user_factors WHERE user_factors.user_id = users.id) AS two_factor
FROM org_members JOIN users ON users.id = org_members.user_id
WHERE org_members.org_id = +sqlc.arg(org_id)
  AND (org_members.expires_at IS NULL OR org_members.expires_at > unixepoch())
ORDER BY org_members.user_id;

-- name: GetOrgMember :one
-- One member as ListOrgMembers lists them.
SELECT sqlc.embed(users), org_members.role, org_members.expires_at,
    CAST((SELECT count(*) FROM sessions WHERE sessions.user_id = users.id AND sessions.expires_at > +sqlc.arg(now) AND NOT sessions.pending) AS INTEGER) AS sessions,
    CAST((SELECT count(*) FROM tokens WHERE tokens.user_id = users.id AND tokens.revoked_at IS NULL
        AND (tokens.expires_at IS NULL OR tokens.expires_at > +sqlc.arg(now))) AS INTEGER) AS tokens,
    EXISTS (SELECT 1 FROM user_factors WHERE user_factors.user_id = users.id) AS two_factor
FROM org_members JOIN users ON users.id = org_members.user_id
WHERE org_members.org_id = +sqlc.arg(org_id) AND org_members.user_id = +sqlc.arg(user_id);

-- GetOrgTokenByName is an org's active manual token by name, the newest
-- when several share it.
-- name: GetOrgTokenByName :one
SELECT * FROM tokens WHERE org_id = +sqlc.arg(org_id) AND name = sqlc.arg(name) AND revoked_at IS NULL AND origin = 'manual'
ORDER BY id DESC LIMIT 1;
