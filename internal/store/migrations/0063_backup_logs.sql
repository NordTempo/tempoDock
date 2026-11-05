-- The output of backup and restore runs (its last 16 KB, secrets masked),
-- for "View log" on a service's Backups tab. A table of its own so the
-- backup lists never read it; trimmed to a service's newest runs as runs
-- finish (core.backupLogRuns).
CREATE TABLE backup_logs (
    backup_id  INTEGER PRIMARY KEY REFERENCES backups (id) ON DELETE CASCADE,
    service_id INTEGER NOT NULL,
    output     TEXT NOT NULL
);
-- Trimming a service's logs to its newest.
CREATE INDEX backup_logs_service ON backup_logs (service_id, backup_id);
