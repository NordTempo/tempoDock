-- Deploy diagnoses (ADR 0071): what explains a failed release, captured
-- when it fails, since its container, exit and health check answer are
-- gone seconds later. bundle is apitypes.Diagnosis as JSON (at most
-- 48 KB), log lines as written: they are masked when read, as logs are.
-- summary is the cause in one line, made from the rule's facts, never log
-- text. A service keeps its newest 20, for build_log_days (ADR 0020).
CREATE TABLE diagnoses (
    id         INTEGER PRIMARY KEY,
    service_id INTEGER NOT NULL REFERENCES services (id),
    release_id INTEGER NOT NULL REFERENCES releases (id),
    kind       TEXT NOT NULL CHECK (kind IN ('release')),
    rule       TEXT NOT NULL DEFAULT '',
    summary    TEXT NOT NULL,
    bundle     TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX diagnoses_service ON diagnoses (service_id, id);
CREATE INDEX diagnoses_release ON diagnoses (release_id);
CREATE INDEX diagnoses_age ON diagnoses (created_at);
