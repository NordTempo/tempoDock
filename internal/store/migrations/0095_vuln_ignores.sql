-- Image vulnerability scanning, phase 2 (ADR 0074): ignores, alerts on
-- new vulnerabilities, and the daily database refresh.

-- An org's decision that a vulnerability doesn't matter where it says:
-- one service (service_id and project_id set), a project (project_id),
-- or the whole org (both 0). package '' is any package. Ignores apply
-- when findings are read, so nothing is scanned again. expires_at NULL
-- is never; expired ignores stay 30 days on the Ignores page, and are
-- deleted when the org next makes one. Times are Unix seconds.
CREATE TABLE vuln_ignores (
    id         INTEGER PRIMARY KEY,
    org_id     INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    project_id INTEGER NOT NULL DEFAULT 0,
    service_id INTEGER NOT NULL DEFAULT 0,
    vuln_id    TEXT NOT NULL,
    package    TEXT NOT NULL DEFAULT '',
    reason     TEXT NOT NULL,
    expires_at INTEGER,
    created_by TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
-- one ignore per place, vulnerability and package: ignoring again
-- replaces its reason and expiry
CREATE UNIQUE INDEX vuln_ignores_key ON vuln_ignores (org_id, project_id, service_id, vuln_id, package);
CREATE INDEX vuln_ignores_expires ON vuln_ignores (expires_at) WHERE expires_at IS NOT NULL;

-- Which new vulnerabilities in live images alert: critical, high (and
-- critical), or off.
ALTER TABLE orgs ADD COLUMN vuln_alerts TEXT NOT NULL DEFAULT 'critical'
    CHECK (vuln_alerts IN ('critical', 'high', 'off'));

-- When the builder last asked for a newer vulnerability database: once
-- a day, at the first quiet minute.
ALTER TABLE servers ADD COLUMN scan_db_checked_at INTEGER;
