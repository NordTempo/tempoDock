-- Single sign-on with OpenID Connect (ADR 0058): orgs' identity
-- providers, their email domains and the identities they vouch for.

-- name: CreateSSOProvider :one
INSERT INTO sso_providers (org_id, name, issuer, client_id, client_secret, jit_role, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetOrgSSOProvider :one
SELECT * FROM sso_providers WHERE org_id = +? AND name = +?;

-- name: ListOrgSSOProviders :many
SELECT * FROM sso_providers WHERE org_id = +? ORDER BY name;

-- ListOrgSSODomains is the email domains of every provider of an org.
-- name: ListOrgSSODomains :many
SELECT sso_provider_domains.provider_id, sso_provider_domains.domain
FROM sso_providers JOIN sso_provider_domains ON sso_provider_domains.provider_id = sso_providers.id
WHERE sso_providers.org_id = +?
ORDER BY sso_provider_domains.domain;

-- name: ListSSOProviderDomains :many
SELECT domain FROM sso_provider_domains WHERE provider_id = +? ORDER BY domain;

-- FindSSOProviders is every provider, in any org, that signs people in
-- for an email domain, with its org: where signing in with an email
-- address starts.
-- name: FindSSOProviders :many
SELECT sqlc.embed(sso_providers), sqlc.embed(orgs)
FROM sso_provider_domains
JOIN sso_providers ON sso_providers.id = sso_provider_domains.provider_id
JOIN orgs ON orgs.id = sso_providers.org_id
WHERE sso_provider_domains.domain = +?;

-- GetSSOProviderByOrg is the provider an org's slug and its name pick,
-- with the org.
-- name: GetSSOProviderByOrg :one
SELECT sqlc.embed(sso_providers), sqlc.embed(orgs)
FROM orgs JOIN sso_providers ON sso_providers.org_id = orgs.id
WHERE orgs.slug = +sqlc.arg(slug) AND sso_providers.name = +sqlc.arg(name);

-- name: GetSSOProviderWithOrg :one
SELECT sqlc.embed(sso_providers), sqlc.embed(orgs)
FROM sso_providers JOIN orgs ON orgs.id = sso_providers.org_id
WHERE sso_providers.id = +?;

-- name: UpdateSSOProvider :one
UPDATE sso_providers SET issuer = ?, client_id = ?, client_secret = ?, jit_role = ?
WHERE id = +? RETURNING *;

-- name: AddSSOProviderDomain :exec
INSERT INTO sso_provider_domains (domain, provider_id) VALUES (?, ?);

-- name: DeleteSSOProviderDomains :exec
DELETE FROM sso_provider_domains WHERE provider_id = +?;

-- Deleting a provider deletes its domains and identities with it.
-- name: DeleteSSOProvider :exec
DELETE FROM sso_providers WHERE id = +?;

-- name: CountOrgSSOProviders :one
SELECT count(*) FROM sso_providers WHERE org_id = +?;

-- name: GetSSOIdentity :one
SELECT * FROM sso_identities WHERE provider_id = +? AND subject = +?;

-- name: CreateSSOIdentity :exec
INSERT INTO sso_identities (provider_id, subject, user_id, email, created_at) VALUES (?, ?, ?, ?, ?);

-- GetOpenOrgInviteByEmail is an org's open invite of an email, the one
-- that lasts longest; it may have expired.
-- name: GetOpenOrgInviteByEmail :one
SELECT * FROM org_invites
WHERE org_id = +? AND email = +? AND accepted_at IS NULL AND revoked_at IS NULL
ORDER BY expires_at DESC LIMIT 1;

-- name: SetOrgSSOOnly :one
UPDATE orgs SET sso_only = ? WHERE id = +? RETURNING *;

-- name: SetOrgSSOOnlyBySlug :one
UPDATE orgs SET sso_only = ? WHERE slug = +? RETURNING *;
