-- foreign_keys: off
-- Cloudflare connections belong to an org or to the platform (ADR 0062,
-- "Connections"). org_id NULL is the platform's: it runs server tunnels
-- and serves Dock's own address; shared lets org domains use its zones.
-- An org's own connection serves only that org's domains. Names are
-- unique per org, the platform's being a namespace of its own. Rebuilt
-- to drop the install-wide UNIQUE on name.
--
-- Existing connections become the platform's, shared, so an install's
-- domains keep working as they did. A new platform connection isn't
-- shared until an operator says so.
CREATE TABLE cloudflare_connections_new (
    id            INTEGER PRIMARY KEY,
    org_id        INTEGER REFERENCES orgs (id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    shared        BOOLEAN NOT NULL DEFAULT 0,
    account_id    TEXT NOT NULL,
    account_name  TEXT NOT NULL DEFAULT '',
    token         BLOB NOT NULL,
    status        TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'invalid')),
    status_reason TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL,
    updated_at    TIMESTAMP NOT NULL,
    updated_by    TEXT NOT NULL
);
INSERT INTO cloudflare_connections_new (id, org_id, name, shared, account_id, account_name, token, status, status_reason, created_at, updated_at, updated_by)
SELECT id, NULL, name, 1, account_id, account_name, token, status, status_reason, created_at, updated_at, updated_by
FROM cloudflare_connections;
DROP TABLE cloudflare_connections;
ALTER TABLE cloudflare_connections_new RENAME TO cloudflare_connections;
CREATE UNIQUE INDEX cloudflare_connections_name ON cloudflare_connections (coalesce(org_id, 0), name);

-- A new token or a refresh tries its connection's failed domains again.
CREATE INDEX cloudflare_records_connection ON cloudflare_records (connection_id);
