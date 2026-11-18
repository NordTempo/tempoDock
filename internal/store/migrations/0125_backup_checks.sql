-- Checked backups (ADR 0096): the weekly repository checks and restore
-- drills, one row per attempt, and the install's switch for them.

CREATE TABLE backup_checks (
    id          INTEGER PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('repository', 'drill')),
    -- server_id is where a repository check ran (a local repository is
    -- that server's; a bucket's check runs on the control plane's). No
    -- foreign key: a deleted server's rows are history nobody lists.
    server_id   INTEGER NOT NULL DEFAULT 0,
    -- service_id is the database a drill restored.
    service_id  INTEGER REFERENCES services (id),
    repository  TEXT NOT NULL,
    -- snapshot_id is the snapshot a drill restored.
    snapshot_id TEXT NOT NULL DEFAULT '',
    trigger     TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual')),
    status      TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'skipped')),
    error       TEXT NOT NULL DEFAULT '',
    -- detail is one line of what was checked: "1.0 GiB read", "42 tables".
    detail      TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMP NOT NULL,
    finished_at TIMESTAMP
);
-- The latest check of a repository on a server, and of a service.
CREATE INDEX backup_checks_repository ON backup_checks (repository, server_id, id) WHERE kind = 'repository';
CREATE INDEX backup_checks_service ON backup_checks (service_id, id) WHERE kind = 'drill';
CREATE INDEX backup_checks_running ON backup_checks (status) WHERE status = 'running';

ALTER TABLE backup_settings ADD COLUMN verify BOOLEAN NOT NULL DEFAULT 1;
