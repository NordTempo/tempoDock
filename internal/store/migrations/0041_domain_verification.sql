-- foreign_keys: off
-- Custom domain verification (ADR 0042). A custom hostname routes only
-- once its org proves control with a TXT record holding verify_token, so
-- several orgs may claim one hostname while none has verified it; the
-- first to verify holds it (the partial unique index). Rebuilds domains
-- to drop hostname's UNIQUE; cloudflare_records references it, so it runs
-- with foreign keys off and is checked before it commits.
--
-- Every existing domain is grandfathered: verified when it was added.
CREATE TABLE domains_new (
    id            INTEGER PRIMARY KEY,
    hostname      TEXT NOT NULL,
    service_id    INTEGER NOT NULL REFERENCES services (id),
    generated     BOOLEAN NOT NULL DEFAULT 0,
    redirect_to   TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'added'
                  CHECK (status IN ('added', 'waiting_for_dns', 'issuing_certificate', 'active', 'error')),
    status_reason TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    verify_token  TEXT NOT NULL DEFAULT '',
    verified_at   TIMESTAMP
);
INSERT INTO domains_new (id, hostname, service_id, generated, redirect_to, status, status_reason, created_at, verified_at)
SELECT id, hostname, service_id, generated, redirect_to, status, status_reason, created_at, created_at FROM domains;
DROP TABLE domains;
ALTER TABLE domains_new RENAME TO domains;
CREATE INDEX domains_service ON domains (service_id);
CREATE INDEX domains_hostname ON domains (hostname);
CREATE UNIQUE INDEX domains_hostname_verified ON domains (hostname) WHERE verified_at IS NOT NULL;
