-- foreign_keys: off
-- Point-in-time restore for Postgres template databases (ADR 0093): a
-- database's archiving setting, and base backups as a kind of backup run
-- (kind 'pitr'), with where their WAL starts and when they are
-- consistent. A restore to a time is a restore run of kind 'pitr' with
-- its target. The CHECK on backups.kind needs the table rebuilt; backup
-- logs reference it, so foreign keys are off while it runs.

CREATE TABLE pitr (
    service_id INTEGER PRIMARY KEY REFERENCES services (id),
    enabled    BOOLEAN NOT NULL,
    -- days of the restorable window to keep (1-35).
    days       INTEGER NOT NULL,
    -- generation names the archive's current folder: a new one starts
    -- with each enable, in-place restore and backup move.
    generation TEXT NOT NULL,
    -- server_id is where the database's archiver runs; NULL when none.
    server_id  INTEGER REFERENCES servers (id),
    -- wal_bytes is the archive's size when retention last listed it.
    wal_bytes  INTEGER NOT NULL DEFAULT 0,
    updated_by TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
CREATE INDEX pitr_enabled ON pitr (service_id) WHERE enabled;
-- Deleting a server checks no archiver is recorded on it.
CREATE INDEX pitr_server ON pitr (server_id);

CREATE TABLE backups_new (
    id                INTEGER PRIMARY KEY,
    service_id        INTEGER NOT NULL REFERENCES services (id),
    operation         TEXT NOT NULL CHECK (operation IN ('backup', 'restore')),
    kind              TEXT NOT NULL CHECK (kind IN ('postgres', 'redis', 'mysql', 'volume', 'pitr')),
    trigger           TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual')),
    status            TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    repository        TEXT NOT NULL,
    snapshot_id       TEXT NOT NULL DEFAULT '',
    size_bytes        INTEGER NOT NULL DEFAULT 0,
    pruned            BOOLEAN NOT NULL DEFAULT 0,
    restored_to       INTEGER REFERENCES services (id),
    error             TEXT NOT NULL DEFAULT '',
    created_by        TEXT NOT NULL,
    started_at        TIMESTAMP NOT NULL,
    finished_at       TIMESTAMP,
    -- A base backup's archive generation, first WAL segment, segment
    -- size and the time it is consistent at (the earliest it restores to).
    wal_generation    TEXT NOT NULL DEFAULT '',
    wal_start         TEXT NOT NULL DEFAULT '',
    wal_segment_bytes INTEGER NOT NULL DEFAULT 0,
    consistent_at     TIMESTAMP,
    -- A restore to a time: the time.
    restore_to_time   TIMESTAMP
);
INSERT INTO backups_new (id, service_id, operation, kind, trigger, status, repository, snapshot_id, size_bytes,
    pruned, restored_to, error, created_by, started_at, finished_at)
SELECT id, service_id, operation, kind, trigger, status, repository, snapshot_id, size_bytes,
    pruned, restored_to, error, created_by, started_at, finished_at FROM backups;
DROP TABLE backups;
ALTER TABLE backups_new RENAME TO backups;
CREATE INDEX backups_service ON backups (service_id, id);
CREATE INDEX backups_status ON backups (status) WHERE status = 'running';
CREATE INDEX backups_snapshot ON backups (repository, snapshot_id);
