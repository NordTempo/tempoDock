-- Single sign-on with OpenID Connect (ADR 0058): an org's identity
-- providers and the email domains they sign people in for, the identities
-- they vouch for, an org's sso-only switch, and which org's provider a
-- session (or the `dock login` token it approved) signed in through.

-- An org's OIDC provider. name addresses it within the org. client_secret
-- is sealed, bound to (org_id, name). jit_role is the role a person the
-- org has never seen gets on their first sign-in ('' for none: they need
-- an invite).
CREATE TABLE sso_providers (
    id            INTEGER PRIMARY KEY,
    org_id        INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    issuer        TEXT NOT NULL,
    client_id     TEXT NOT NULL,
    client_secret BLOB NOT NULL,
    jit_role      TEXT NOT NULL DEFAULT '' CHECK (jit_role IN ('', 'read', 'deploy')),
    created_at    DATETIME NOT NULL,
    created_by    TEXT NOT NULL,
    UNIQUE (org_id, name)
);

-- The email domains a provider signs people in for (lowercase). Signing
-- in starts from an email address and finds providers by its domain, in
-- every org; provider_id leads the second index for listing and cascades.
CREATE TABLE sso_provider_domains (
    domain      TEXT NOT NULL,
    provider_id INTEGER NOT NULL REFERENCES sso_providers (id) ON DELETE CASCADE,
    PRIMARY KEY (domain, provider_id)
) WITHOUT ROWID;
CREATE INDEX sso_provider_domains_provider ON sso_provider_domains (provider_id);

-- A person at a provider (its sub claim), linked to a Dock user.
CREATE TABLE sso_identities (
    provider_id INTEGER NOT NULL REFERENCES sso_providers (id) ON DELETE CASCADE,
    subject     TEXT NOT NULL,
    user_id     INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    email       TEXT NOT NULL,
    created_at  DATETIME NOT NULL,
    PRIMARY KEY (provider_id, subject)
) WITHOUT ROWID;
CREATE INDEX sso_identities_user ON sso_identities (user_id);

-- An open invite of an email, found when that person first signs in
-- through the org's provider.
CREATE INDEX org_invites_open_email ON org_invites (org_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- Members act in the org only with a session that signed in through one
-- of its providers.
ALTER TABLE orgs ADD COLUMN sso_only BOOLEAN NOT NULL DEFAULT 0;

-- The org whose provider a sign-in went through; NULL for a password. Such
-- a session acts only in that org, never as an operator. The `dock login`
-- token it approves carries it.
ALTER TABLE sessions ADD COLUMN sso_org_id INTEGER REFERENCES orgs (id) ON DELETE CASCADE;
ALTER TABLE cli_logins ADD COLUMN sso_org_id INTEGER REFERENCES orgs (id) ON DELETE CASCADE;
ALTER TABLE tokens ADD COLUMN sso_org_id INTEGER REFERENCES orgs (id) ON DELETE CASCADE;
-- For deleting an org's SSO sessions and tokens with it.
CREATE INDEX sessions_sso_org ON sessions (sso_org_id) WHERE sso_org_id IS NOT NULL;
CREATE INDEX tokens_sso_org ON tokens (sso_org_id) WHERE sso_org_id IS NOT NULL;
