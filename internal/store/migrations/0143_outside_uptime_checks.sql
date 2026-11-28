-- foreign_keys: off
-- Outside uptime checks (ADR 0055, 2026-11-28 amendment): a check of a
-- site outside Dock belongs to its organization (org_id) instead of a
-- service, for status page components. service_id becomes nullable, so
-- the table is rebuilt; uptime_stats and status_sources reference it.
CREATE TABLE uptime_checks_new (
    id            INTEGER PRIMARY KEY,
    service_id    INTEGER REFERENCES services (id) ON DELETE CASCADE,
    -- Set for an outside check only: a service's check is its service's
    -- organization's, through its project.
    org_id        INTEGER REFERENCES orgs (id) ON DELETE CASCADE,
    url           TEXT NOT NULL,
    expect_status INTEGER NOT NULL,
    interval_s    INTEGER NOT NULL,
    timeout_ms    INTEGER NOT NULL,
    created_at    DATETIME NOT NULL,
    created_by    TEXT NOT NULL,
    auto          BOOLEAN NOT NULL DEFAULT 0,
    CHECK ((service_id IS NULL) != (org_id IS NULL))
);
INSERT INTO uptime_checks_new (id, service_id, org_id, url, expect_status, interval_s, timeout_ms, created_at, created_by, auto)
SELECT id, service_id, NULL, url, expect_status, interval_s, timeout_ms, created_at, created_by, auto FROM uptime_checks;
DROP TABLE uptime_checks;
ALTER TABLE uptime_checks_new RENAME TO uptime_checks;
CREATE INDEX uptime_checks_service ON uptime_checks (service_id);
CREATE INDEX uptime_checks_org ON uptime_checks (org_id) WHERE org_id IS NOT NULL;
