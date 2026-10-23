-- Invites and org variables (ADR 0027 phase 2).

-- An invite to an org for one email address. hash is the SHA-256 of the
-- secret code in the invite link; the code works once (accepted_at), until
-- expires_at, unless revoked, and only for a user signed in with email.
CREATE TABLE org_invites (
    id          INTEGER PRIMARY KEY,
    org_id      INTEGER NOT NULL REFERENCES orgs (id),
    email       TEXT NOT NULL COLLATE NOCASE,
    role        TEXT NOT NULL CHECK (role IN ('read', 'deploy', 'admin')),
    hash        TEXT NOT NULL UNIQUE,
    created_by  TEXT NOT NULL,
    created_at  DATETIME NOT NULL,
    expires_at  DATETIME NOT NULL,
    accepted_at DATETIME,
    revoked_at  DATETIME
);
-- An org's open invites, listed with its members.
CREATE INDEX org_invites_open ON org_invites (org_id, id) WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- Variables every project of an org may reference as ${{org.KEY}}. value
-- is sealed like other variable values; changing one takes effect on each
-- referencing service's next release.
CREATE TABLE org_variables (
    org_id     INTEGER NOT NULL REFERENCES orgs (id),
    key        TEXT NOT NULL,
    value      BLOB NOT NULL,
    updated_at DATETIME NOT NULL,
    updated_by TEXT NOT NULL,
    PRIMARY KEY (org_id, key)
) WITHOUT ROWID;
