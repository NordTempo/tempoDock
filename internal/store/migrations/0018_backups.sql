-- Database templates and backups (ADR 0019).

-- The template a service was created from: '' for none, else 'postgres' or
-- 'redis'. It drives health checks, backups, restores and data setup.
ALTER TABLE services ADD COLUMN template TEXT NOT NULL DEFAULT ''
    CHECK (template IN ('', 'postgres', 'redis'));

-- The install's backup settings, one row (id 1), created on first use.
-- repository '' is the local repository on each server (not off-box);
-- credentials (sealed JSON) are the S3 access key and secret; password
-- (sealed) is the restic repository password. hour is when scheduled
-- backups run, in the control plane's local time.
CREATE TABLE backup_settings (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    repository   TEXT NOT NULL DEFAULT '',
    credentials  BLOB,
    password     BLOB NOT NULL,
    keep_daily   INTEGER NOT NULL DEFAULT 7,
    keep_weekly  INTEGER NOT NULL DEFAULT 4,
    keep_monthly INTEGER NOT NULL DEFAULT 6,
    hour         INTEGER NOT NULL DEFAULT 3 CHECK (hour BETWEEN 0 AND 23),
    updated_at   TIMESTAMP NOT NULL,
    updated_by   TEXT NOT NULL DEFAULT ''
);

-- Per-service schedule overrides; a stateful service without a row backs
-- up daily at the install's hour. hour NULL follows the install's.
CREATE TABLE backup_schedules (
    service_id INTEGER PRIMARY KEY REFERENCES services (id),
    enabled    BOOLEAN NOT NULL DEFAULT 1,
    hour       INTEGER CHECK (hour BETWEEN 0 AND 23)
);

-- Backup and restore runs. A restore row belongs to the service whose
-- backup it read (service_id) and names the service it created
-- (restored_to). repository is where the snapshot is ('' = the local one on
-- the service's server). pruned marks snapshots retention removed.
CREATE TABLE backups (
    id          INTEGER PRIMARY KEY,
    service_id  INTEGER NOT NULL REFERENCES services (id),
    operation   TEXT NOT NULL CHECK (operation IN ('backup', 'restore')),
    kind        TEXT NOT NULL CHECK (kind IN ('postgres', 'redis', 'volume')),
    trigger     TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual')),
    status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    repository  TEXT NOT NULL,
    snapshot_id TEXT NOT NULL DEFAULT '',
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    pruned      BOOLEAN NOT NULL DEFAULT 0,
    restored_to INTEGER REFERENCES services (id),
    error       TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL,
    started_at  TIMESTAMP NOT NULL,
    finished_at TIMESTAMP
);
-- A service's runs newest first, and its latest scheduled attempt.
CREATE INDEX backups_service ON backups (service_id, id);
CREATE INDEX backups_status ON backups (status) WHERE status = 'running';
