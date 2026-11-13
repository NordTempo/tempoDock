-- foreign_keys: off
-- Deleting an archived project (ADR 0077) removes its rows, and SQLite
-- hands a new row the highest ID in use plus one: a project, environment,
-- service or site created after the newest one was deleted would get its
-- ID, and with it what outlives the rows under that ID (analytics events
-- by site, traces and metrics by service, trace tokens, Docker labels and
-- names, restic tags, log archives on servers). AUTOINCREMENT never hands
-- out an ID twice. The tables are rebuilt as they are, column for column,
-- with their indexes; foreign keys are off while they are swapped and
-- checked before the migration commits.

CREATE TABLE projects_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id      INTEGER NOT NULL REFERENCES orgs (id),
    name        TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at DATETIME,
    UNIQUE (org_id, name)
);
INSERT INTO projects_new SELECT * FROM projects;
DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;

CREATE TABLE environments_new (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id          INTEGER NOT NULL REFERENCES projects (id),
    name                TEXT NOT NULL,
    server_id           INTEGER NOT NULL REFERENCES servers (id),
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    network             TEXT NOT NULL DEFAULT '',
    apply_error         TEXT NOT NULL DEFAULT '',
    kind                TEXT NOT NULL DEFAULT 'non_production'
                        CHECK (kind IN ('production', 'non_production')),
    locked_at           TIMESTAMP,
    locked_by           TEXT NOT NULL DEFAULT '',
    lock_reason         TEXT NOT NULL DEFAULT '',
    open_until          TIMESTAMP,
    archived_at         DATETIME,
    sleep_after_minutes INTEGER NOT NULL DEFAULT 0,
    asleep_at           DATETIME,
    slept_by            TEXT NOT NULL DEFAULT '',
    freed_bytes         INTEGER NOT NULL DEFAULT 0,
    woke_at             DATETIME,
    woke_by             TEXT NOT NULL DEFAULT '',
    UNIQUE (project_id, name)
);
INSERT INTO environments_new SELECT * FROM environments;
DROP TABLE environments;
ALTER TABLE environments_new RENAME TO environments;
CREATE UNIQUE INDEX environments_network ON environments (network) WHERE network != '';
CREATE INDEX environments_server ON environments (server_id);
CREATE INDEX environments_sleepable ON environments (id)
WHERE sleep_after_minutes > 0 OR asleep_at IS NOT NULL;

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
                                       CHECK (template IN ('', 'postgres', 'redis')),
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
    UNIQUE (environment_id, name)
);
INSERT INTO services_new SELECT * FROM services;
DROP TABLE services;
ALTER TABLE services_new RENAME TO services;

CREATE TABLE sites_new (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    environment_id INTEGER NOT NULL REFERENCES environments (id),
    name           TEXT NOT NULL,
    -- The site's own settings, as JSON; a missing key follows the
    -- install's defaults (analytics_settings.site_defaults).
    settings       TEXT NOT NULL DEFAULT '{}',
    archived_at    TIMESTAMP,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMP NOT NULL,
    public_slug    TEXT
);
INSERT INTO sites_new SELECT * FROM sites;
DROP TABLE sites;
ALTER TABLE sites_new RENAME TO sites;
CREATE INDEX sites_environment ON sites (environment_id);
CREATE UNIQUE INDEX sites_live_name ON sites (environment_id, name) WHERE archived_at IS NULL;
CREATE UNIQUE INDEX sites_live_public_domain ON sites (json_extract(settings, '$.public_domain'))
WHERE archived_at IS NULL AND json_extract(settings, '$.public_domain') <> '';
CREATE UNIQUE INDEX sites_live_public_slug ON sites (public_slug) WHERE archived_at IS NULL;
