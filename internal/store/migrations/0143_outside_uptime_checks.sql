-- foreign_keys: off
-- Outside uptime checks (ADR 0055, 2026-11-28 amendment): a check of a
-- site outside Dock belongs to its organization (org_id) instead of a
-- service, for status page components. service_id becomes nullable, so
-- the table is rebuilt; uptime_stats and status_sources reference it.
-- AUTOINCREMENT: a check's ID is never another's later, since its alerts
-- (keyed uptime:<id>) outlive it and outside checks of another org could
-- otherwise inherit them.
CREATE TABLE uptime_checks_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
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
-- IDs start past every one an alert still names, removed checks' too.
INSERT INTO sqlite_sequence (name, seq) SELECT 'uptime_checks', 0
WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'uptime_checks');
UPDATE sqlite_sequence SET seq = max(seq, (SELECT coalesce(max(CAST(substr(key, 8) AS INTEGER)), 0) FROM alerts WHERE key LIKE 'uptime:%'))
WHERE name = 'uptime_checks';

-- The UI's organization page Status is /<org>/status now (ADR 0073,
-- 2026-11-28), which would shadow a project named status: such a
-- project becomes status-<id> (with a random suffix when its
-- organization has that name already), as `dock project rename` would
-- (its ID, services, domains and history stay; its open alerts carry
-- the new name), and its organization's Activity says so.
CREATE TEMP TABLE status_renames AS
SELECT p.id, p.org_id,
  CASE WHEN EXISTS (SELECT 1 FROM projects AS o WHERE o.org_id = p.org_id AND o.name = 'status-' || p.id)
    THEN 'status-' || p.id || '-' || lower(hex(randomblob(3)))
    ELSE 'status-' || p.id END AS name
FROM projects AS p WHERE p.name = 'status';
INSERT INTO audit_events (actor, actor_kind, action, project_id, target, summary, org_id)
SELECT 'system', 'system', 'project.rename', id, name,
  'renamed project status to ' || name || ': /<organization>/status is the Status page now; rename it again with `dock project rename`', org_id
FROM status_renames;
UPDATE alerts SET project = (SELECT name FROM status_renames WHERE status_renames.id = alerts.project_id)
WHERE project = 'status' AND project_id IN (SELECT id FROM status_renames);
UPDATE projects SET name = (SELECT name FROM status_renames WHERE status_renames.id = projects.id)
WHERE id IN (SELECT id FROM status_renames);
DROP TABLE status_renames;
