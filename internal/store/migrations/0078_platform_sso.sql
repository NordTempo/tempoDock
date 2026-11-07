-- foreign_keys: off
-- Single sign-on is the platform's (ADR 0065, amending 0058): operators
-- set up Google, GitHub and custom OpenID Connect providers for the whole
-- Dock, the sign-in page shows a button for each, and orgs only decide
-- membership. Providers lose their org, email domains, just-in-time role
-- and orgs' sso-only switch; sessions remember the provider instead of an
-- org; two platform switches decide whether passwords sign in and whether
-- a provider may create accounts for people nobody invited.
--
-- Rebuilt to drop org_id: each org's providers become the platform's,
-- keeping their ids (and so their identities). A name taken by an older
-- provider gets "-<org slug>" (or, if that is taken too, "-<id>").
CREATE TABLE sso_providers_new (
    id            INTEGER PRIMARY KEY,
    -- name addresses the provider in the API and CLI.
    name          TEXT NOT NULL UNIQUE,
    -- kind: google and oidc speak OpenID Connect (google's issuer is
    -- fixed), github is GitHub's OAuth with its API for the email.
    kind          TEXT NOT NULL CHECK (kind IN ('google', 'github', 'oidc')),
    -- label is what the sign-in page's button names: "Continue with <label>".
    label         TEXT NOT NULL,
    -- issuer is '' for github.
    issuer        TEXT NOT NULL,
    client_id     TEXT NOT NULL,
    -- client_secret is sealed, bound to name.
    client_secret BLOB NOT NULL,
    created_at    DATETIME NOT NULL,
    created_by    TEXT NOT NULL
);
INSERT INTO sso_providers_new (id, name, kind, label, issuer, client_id, client_secret, created_at, created_by)
SELECT p.id,
    CASE
        WHEN NOT EXISTS (SELECT 1 FROM sso_providers o WHERE o.name = p.name AND o.id < p.id) THEN p.name
        WHEN NOT EXISTS (SELECT 1 FROM sso_providers o WHERE o.name = p.name || '-' || orgs.slug) THEN p.name || '-' || orgs.slug
        ELSE p.name || '-' || p.id
    END,
    CASE WHEN p.issuer = 'https://accounts.google.com' THEN 'google' ELSE 'oidc' END,
    CASE WHEN p.issuer = 'https://accounts.google.com' THEN 'Google' ELSE upper(substr(p.name, 1, 1)) || substr(p.name, 2) END,
    p.issuer, p.client_id, p.client_secret, p.created_at, p.created_by
FROM sso_providers p JOIN orgs ON orgs.id = p.org_id;

-- The secrets above are still sealed for (org_id, old name). Core opens
-- each with that, seals it again for its new place and deletes the row,
-- at its next start (resealSSOSecrets).
CREATE TABLE sso_reseal (
    provider_id INTEGER PRIMARY KEY REFERENCES sso_providers (id) ON DELETE CASCADE,
    org_id      INTEGER NOT NULL,
    name        TEXT NOT NULL
);
INSERT INTO sso_reseal (provider_id, org_id, name) SELECT id, org_id, name FROM sso_providers;

-- Sessions remember the provider they signed in through (NULL for a
-- password); removing the provider ends them. An org's sign-in maps to
-- that org's first provider.
ALTER TABLE sessions ADD COLUMN sso_provider_id INTEGER REFERENCES sso_providers (id) ON DELETE CASCADE;
UPDATE sessions SET sso_provider_id = (SELECT min(id) FROM sso_providers WHERE org_id = sessions.sso_org_id)
WHERE sso_org_id IS NOT NULL;
CREATE INDEX sessions_sso_provider ON sessions (sso_provider_id) WHERE sso_provider_id IS NOT NULL;

DROP TABLE sso_provider_domains;
DROP TABLE sso_providers;
ALTER TABLE sso_providers_new RENAME TO sso_providers;

-- Nothing binds a sign-in to an org any more.
DROP INDEX sessions_sso_org;
DROP INDEX tokens_sso_org;
ALTER TABLE sessions DROP COLUMN sso_org_id;
ALTER TABLE tokens DROP COLUMN sso_org_id;
ALTER TABLE cli_logins DROP COLUMN sso_org_id;
ALTER TABLE orgs DROP COLUMN sso_only;

-- A person's open invites in every org, found by their email when a
-- provider signs them in for the first time.
DROP INDEX org_invites_open_email;
CREATE INDEX org_invites_open_email ON org_invites (email) WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- password_sign_in: the sign-in page takes passwords (off only once a
-- provider works; `dock admin password-sign-in on` turns it back on from
-- the server). invite_only: a provider creates an account only for an
-- email with an open invite.
ALTER TABLE platform_settings ADD COLUMN password_sign_in BOOLEAN NOT NULL DEFAULT 1;
ALTER TABLE platform_settings ADD COLUMN invite_only BOOLEAN NOT NULL DEFAULT 1;
