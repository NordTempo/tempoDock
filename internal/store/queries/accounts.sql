-- The platform console's Users page (ADR 0062): user accounts, which
-- are the platform's, with the orgs each belongs to.

-- name: ListAccounts :many
-- Every user account with whether it has a second factor, by email.
SELECT sqlc.embed(users),
    EXISTS (SELECT 1 FROM user_factors WHERE user_factors.user_id = users.id) AS two_factor
FROM users ORDER BY users.email;

-- name: ListAccountMemberships :many
-- Every membership that hasn't expired, with its org's slug, for the
-- accounts list; an org's first admin of its own is its owner.
SELECT org_members.user_id, orgs.slug, org_members.role, org_members.expires_at,
    CAST(coalesce(org_members.user_id = (SELECT first.user_id FROM org_members first
        WHERE first.org_id = org_members.org_id AND first.role = 'admin' AND first.expires_at IS NULL
        ORDER BY first.created_at LIMIT 1), 0) AS BOOLEAN) AS owner
FROM org_members JOIN orgs ON orgs.id = org_members.org_id
WHERE orgs.deleted_at IS NULL
  AND (org_members.expires_at IS NULL OR org_members.expires_at > unixepoch())
ORDER BY org_members.user_id, orgs.slug;

-- SetUserPlatformRole gives a user a platform role ('operator',
-- 'viewer') or takes it away (NULL); ADR 0080.
-- name: SetUserPlatformRole :exec
UPDATE users SET platform_role = ? WHERE id = +?;

-- name: CountOtherOperators :one
-- Operators who can sign in, other than user_id: the platform keeps one.
SELECT count(*) FROM users WHERE platform_role = 'operator' AND disabled_at IS NULL AND id != +?;
