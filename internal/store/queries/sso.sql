-- Single sign-on (ADR 0058, 0065): the platform's identity providers, the
-- identities they vouch for, and the platform's sign-in switches.

-- name: CreateSSOProvider :one
INSERT INTO sso_providers (name, kind, label, issuer, client_id, client_secret, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSSOProvider :one
SELECT * FROM sso_providers WHERE id = +?;

-- name: GetSSOProviderByName :one
SELECT * FROM sso_providers WHERE name = +?;

-- ListSSOProviders is every provider in the order they were added: the
-- sign-in page's buttons.
-- name: ListSSOProviders :many
SELECT * FROM sso_providers ORDER BY id;

-- ListSSOProvidersLinked is every provider with how many people are
-- linked through it, for the console.
-- name: ListSSOProvidersLinked :many
SELECT sqlc.embed(sso_providers), (SELECT count(*) FROM sso_identities WHERE sso_identities.provider_id = sso_providers.id) AS linked
FROM sso_providers ORDER BY sso_providers.id;

-- name: UpdateSSOProvider :one
UPDATE sso_providers SET label = ?, issuer = ?, client_id = ?, client_secret = ?
WHERE id = +? RETURNING *;

-- Deleting a provider deletes its identities and sessions with it.
-- name: DeleteSSOProvider :exec
DELETE FROM sso_providers WHERE id = +?;

-- name: CountSSOProviders :one
SELECT count(*) FROM sso_providers;

-- name: GetSSOIdentity :one
SELECT * FROM sso_identities WHERE provider_id = +? AND subject = +?;

-- name: CreateSSOIdentity :exec
INSERT INTO sso_identities (provider_id, subject, user_id, email, created_at) VALUES (?, ?, ?, ?, ?);

-- ListOpenInvitesByEmail is every open, unexpired invite of an email,
-- with its org: what a provider's first sign-in of that person accepts.
-- name: ListOpenInvitesByEmail :many
SELECT sqlc.embed(org_invites), sqlc.embed(orgs)
FROM org_invites JOIN orgs ON orgs.id = org_invites.org_id
WHERE org_invites.email = +sqlc.arg(email) AND org_invites.accepted_at IS NULL AND org_invites.revoked_at IS NULL
    AND org_invites.expires_at > +sqlc.arg(now) AND orgs.deleted_at IS NULL
ORDER BY org_invites.id;

-- GetSignInSettings is the platform's sign-in switches; no row until an
-- operator first sets something (both on then).
-- name: GetSignInSettings :one
SELECT password_sign_in, invite_only FROM platform_settings WHERE id = 1;

-- name: SetSignInSettings :exec
INSERT INTO platform_settings (id, password_sign_in, invite_only, updated_by, updated_at) VALUES (1, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    password_sign_in = excluded.password_sign_in, invite_only = excluded.invite_only,
    updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- ListSSOReseal is the client secrets migration 0078 left sealed for
-- their org's provider (ADR 0058), to seal again as the platform's.
-- name: ListSSOReseal :many
SELECT sqlc.embed(sso_reseal), sqlc.embed(sso_providers)
FROM sso_reseal JOIN sso_providers ON sso_providers.id = sso_reseal.provider_id;

-- name: SetSSOProviderSecret :exec
UPDATE sso_providers SET client_secret = ? WHERE id = +?;

-- name: DeleteSSOReseal :exec
DELETE FROM sso_reseal WHERE provider_id = +?;
