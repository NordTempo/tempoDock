-- The control plane's own backups (ADR 0021): one row (id 1) for the
-- latest run, so the table never grows. scheduled_at is when the latest
-- scheduled attempt started, which the scheduler counts from; manual runs
-- (`dock backup run --control-plane`) don't move it.
CREATE TABLE control_plane_backup (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    trigger      TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual')),
    status       TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    repository   TEXT NOT NULL,
    snapshot_id  TEXT NOT NULL DEFAULT '',
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL,
    started_at   TIMESTAMP NOT NULL,
    finished_at  TIMESTAMP,
    scheduled_at TIMESTAMP
);
