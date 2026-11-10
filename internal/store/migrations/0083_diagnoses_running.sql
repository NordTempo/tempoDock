-- Deploy diagnoses of running services (ADR 0071, phase 2): a crash loop
-- and an unhealthy service after a restart didn't help get one too, kept
-- with their active release. Rebuilds diagnoses for its kinds; nothing
-- references it.
CREATE TABLE diagnoses_new (
    id         INTEGER PRIMARY KEY,
    service_id INTEGER NOT NULL REFERENCES services (id),
    release_id INTEGER NOT NULL REFERENCES releases (id),
    kind       TEXT NOT NULL CHECK (kind IN ('release', 'crash_loop', 'unhealthy')),
    rule       TEXT NOT NULL DEFAULT '',
    summary    TEXT NOT NULL,
    bundle     TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
INSERT INTO diagnoses_new SELECT id, service_id, release_id, kind, rule, summary, bundle, created_at FROM diagnoses;
DROP TABLE diagnoses;
ALTER TABLE diagnoses_new RENAME TO diagnoses;
CREATE INDEX diagnoses_service ON diagnoses (service_id, id);
CREATE INDEX diagnoses_release ON diagnoses (release_id);
CREATE INDEX diagnoses_age ON diagnoses (created_at);
