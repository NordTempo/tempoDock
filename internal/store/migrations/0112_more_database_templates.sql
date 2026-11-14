-- foreign_keys: off
-- Database templates for MySQL, MariaDB, Valkey and Postgres with
-- pgvector (ADR 0082), and MySQL dumps as a backup kind: the CHECKs on
-- services.template and backups.kind list them. SQLite can't change a
-- CHECK, so both tables are rebuilt as they are, column for column, with
-- their indexes; foreign keys are off while they are swapped and checked
-- before the migration commits.

CREATE TABLE services_new (
    id                                 INTEGER PRIMARY KEY AUTOINCREMENT,
    environment_id                     INTEGER NOT NULL REFERENCES environments (id),
    name                               TEXT NOT NULL,
    image                              TEXT NOT NULL DEFAULT '',
    port                               INTEGER NOT NULL DEFAULT 0,
    health_check_path                  TEXT NOT NULL DEFAULT '',
    health_check_timeout_seconds       INTEGER NOT NULL DEFAULT 0,
    start_command                      TEXT NOT NULL DEFAULT '',
    dockerfile_path                    TEXT NOT NULL DEFAULT '',
    build_command                      TEXT NOT NULL DEFAULT '',
    memory_mb                          INTEGER NOT NULL DEFAULT 0,
    cpus                               REAL NOT NULL DEFAULT 0,
    -- Staged settings (JSON of the full next settings), applied on deploy or
    -- an explicit apply. NULL means nothing is staged.
    pending_settings                   TEXT,
    created_at                         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at                        DATETIME,
    database_url_variable              TEXT NOT NULL DEFAULT '',
    template                           TEXT NOT NULL DEFAULT ''
                                       CHECK (template IN ('', 'postgres', 'redis', 'mysql', 'mariadb', 'valkey', 'pgvector')),
    health_check_interval_seconds      INTEGER NOT NULL DEFAULT 0,
    health_check_probe_timeout_seconds INTEGER NOT NULL DEFAULT 0,
    health_check_retries               INTEGER NOT NULL DEFAULT 0,
    health_check_start_period_seconds  INTEGER NOT NULL DEFAULT 0,
    pre_deploy_command                 TEXT NOT NULL DEFAULT '',
    pre_deploy_timeout_seconds         INTEGER NOT NULL DEFAULT 0,
    staged                             TEXT NOT NULL DEFAULT ''
                                       CHECK (staged IN ('', 'create', 'archive')),
    stopped_at                         DATETIME,
    stopped_by                         TEXT NOT NULL DEFAULT '',
    pre_deploy_snapshot                BOOLEAN NOT NULL DEFAULT 0,
    database                           TEXT NOT NULL DEFAULT ''
                                       CHECK (database IN ('', 'postgres', 'mysql', 'redis', 'none')),
    from_template                      TEXT NOT NULL DEFAULT '',
    from_template_revision             INTEGER NOT NULL DEFAULT 0,
    UNIQUE (environment_id, name)
);
INSERT INTO services_new SELECT * FROM services;
DROP TABLE services;
ALTER TABLE services_new RENAME TO services;

CREATE TABLE backups_new (
    id          INTEGER PRIMARY KEY,
    service_id  INTEGER NOT NULL REFERENCES services (id),
    operation   TEXT NOT NULL CHECK (operation IN ('backup', 'restore')),
    kind        TEXT NOT NULL CHECK (kind IN ('postgres', 'redis', 'mysql', 'volume')),
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
INSERT INTO backups_new SELECT * FROM backups;
DROP TABLE backups;
ALTER TABLE backups_new RENAME TO backups;
CREATE INDEX backups_service ON backups (service_id, id);
CREATE INDEX backups_status ON backups (status) WHERE status = 'running';
CREATE INDEX backups_snapshot ON backups (repository, snapshot_id);
