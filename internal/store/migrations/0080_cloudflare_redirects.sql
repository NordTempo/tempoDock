-- Redirects on Cloudflare zones (ADR 0068): a hostname in one of a
-- connection's zones that Cloudflare redirects to a URL at its edge, with
-- a proxied placeholder record and a Single Redirect rule Dock makes. A
-- redirect is its connection's owner's (the platform's or an org's), so
-- it has no org of its own. The worker (cloudflare_queue.go) makes
-- Cloudflare match, as for cloudflare_records.
CREATE TABLE cloudflare_redirects (
    id            INTEGER PRIMARY KEY,
    hostname      TEXT NOT NULL UNIQUE,
    connection_id INTEGER NOT NULL REFERENCES cloudflare_connections (id) ON DELETE CASCADE,
    zone_id       TEXT NOT NULL,
    target        TEXT NOT NULL,
    status_code   INTEGER NOT NULL DEFAULT 302 CHECK (status_code IN (301, 302, 307, 308)),
    -- keep_path appends the request's path and query to target.
    keep_path     BOOLEAN NOT NULL DEFAULT 0,
    -- What Dock made in Cloudflare, once known.
    record_id     TEXT NOT NULL DEFAULT '',
    ruleset_id    TEXT NOT NULL DEFAULT '',
    rule_id       TEXT NOT NULL DEFAULT '',
    want          TEXT NOT NULL DEFAULT 'present' CHECK (want IN ('present', 'absent')),
    status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ok', 'error')),
    reason        TEXT NOT NULL DEFAULT '',
    attempts      INTEGER NOT NULL DEFAULT 0,
    retry_at      TIMESTAMP,
    gen           INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMP NOT NULL,
    updated_at    TIMESTAMP NOT NULL,
    updated_by    TEXT NOT NULL
);
CREATE INDEX cloudflare_redirects_due ON cloudflare_redirects (status, retry_at);
CREATE INDEX cloudflare_redirects_connection ON cloudflare_redirects (connection_id);
