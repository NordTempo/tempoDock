-- foreign_keys: off
-- Named identity providers (ADR 0081): microsoft, gitlab, okta, auth0,
-- keycloak and authentik are OpenID Connect providers whose issuer Dock
-- builds from a tenant, a domain or a realm. Only the kind's CHECK
-- changes; SQLite rebuilds the table for that, keeping every id (and so
-- the identities and sessions that reference it).
CREATE TABLE sso_providers_new (
    id            INTEGER PRIMARY KEY,
    -- name addresses the provider in the API and CLI.
    name          TEXT NOT NULL UNIQUE,
    -- kind: github is GitHub's OAuth with its API for the email; every
    -- other kind speaks OpenID Connect. oidc takes any issuer, the rest
    -- build theirs (google's is fixed).
    kind          TEXT NOT NULL CHECK (kind IN ('google', 'github', 'oidc', 'microsoft', 'gitlab', 'okta', 'auth0', 'keycloak', 'authentik')),
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
SELECT id, name, kind, label, issuer, client_id, client_secret, created_at, created_by FROM sso_providers;
DROP TABLE sso_providers;
ALTER TABLE sso_providers_new RENAME TO sso_providers;
