-- Initial schema. Timestamps are UTC. Secrets (variable values, resolved
-- release config) are AES-GCM ciphertext from internal/secrets.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    name          TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('read', 'deploy', 'admin')),
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users (id),
    hash         TEXT NOT NULL UNIQUE,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   DATETIME NOT NULL
);

CREATE TABLE servers (
    id           INTEGER PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    role         TEXT NOT NULL CHECK (role IN ('production', 'non-production')),
    builder      BOOLEAN NOT NULL DEFAULT 0,
    local        BOOLEAN NOT NULL DEFAULT 0,
    status       TEXT NOT NULL DEFAULT 'online' CHECK (status IN ('online', 'offline')),
    last_seen_at DATETIME,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE projects (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE environments (
    id         INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects (id),
    name       TEXT NOT NULL,
    server_id  INTEGER NOT NULL REFERENCES servers (id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, name)
);

CREATE TABLE tokens (
    id             INTEGER PRIMARY KEY,
    name           TEXT NOT NULL,
    hash           TEXT NOT NULL UNIQUE,
    scope          TEXT NOT NULL CHECK (scope IN ('read', 'deploy', 'admin')),
    project_id     INTEGER REFERENCES projects (id),
    environment_id INTEGER REFERENCES environments (id),
    created_by     TEXT NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at     DATETIME,
    last_used_at   DATETIME,
    revoked_at     DATETIME
);

CREATE TABLE services (
    id                           INTEGER PRIMARY KEY,
    environment_id               INTEGER NOT NULL REFERENCES environments (id),
    name                         TEXT NOT NULL,
    image                        TEXT NOT NULL DEFAULT '',
    port                         INTEGER NOT NULL DEFAULT 0,
    health_check_path            TEXT NOT NULL DEFAULT '',
    health_check_timeout_seconds INTEGER NOT NULL DEFAULT 0,
    start_command                TEXT NOT NULL DEFAULT '',
    dockerfile_path              TEXT NOT NULL DEFAULT '',
    build_command                TEXT NOT NULL DEFAULT '',
    memory_mb                    INTEGER NOT NULL DEFAULT 0,
    cpus                         REAL NOT NULL DEFAULT 0,
    -- Staged settings (JSON of the full next settings), applied on deploy or
    -- an explicit apply. NULL means nothing is staged.
    pending_settings             TEXT,
    created_at                   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at                  DATETIME,
    UNIQUE (environment_id, name)
);

-- service_id NULL means an environment-level (shared) variable.
-- value is the live value (NULL: not live yet). pending is a staged change:
-- 'set' to pending_value, or 'unset'.
CREATE TABLE variables (
    id             INTEGER PRIMARY KEY,
    environment_id INTEGER NOT NULL REFERENCES environments (id),
    service_id     INTEGER REFERENCES services (id),
    key            TEXT NOT NULL,
    value          BLOB,
    pending        TEXT NOT NULL DEFAULT '' CHECK (pending IN ('', 'set', 'unset')),
    pending_value  BLOB,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX variables_scope_key ON variables (environment_id, coalesce(service_id, 0), key);

CREATE TABLE domains (
    id            INTEGER PRIMARY KEY,
    hostname      TEXT NOT NULL UNIQUE,
    service_id    INTEGER NOT NULL REFERENCES services (id),
    generated     BOOLEAN NOT NULL DEFAULT 0,
    redirect_to   TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'added'
                  CHECK (status IN ('added', 'waiting_for_dns', 'issuing_certificate', 'active', 'error')),
    status_reason TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX domains_service ON domains (service_id);

-- Volumes are never deleted as a side effect of anything else.
CREATE TABLE volumes (
    id          INTEGER PRIMARY KEY,
    service_id  INTEGER NOT NULL REFERENCES services (id),
    name        TEXT NOT NULL,
    docker_name TEXT NOT NULL UNIQUE,
    mount_path  TEXT NOT NULL,
    -- Staged volumes aren't mounted until applied.
    pending     BOOLEAN NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (service_id, name),
    UNIQUE (service_id, mount_path)
);

CREATE TABLE releases (
    id             INTEGER PRIMARY KEY,
    service_id     INTEGER NOT NULL REFERENCES services (id),
    number         INTEGER NOT NULL,
    status         TEXT NOT NULL DEFAULT 'queued'
                   CHECK (status IN ('queued', 'building', 'starting', 'health_checking', 'active', 'failed', 'superseded')),
    source         TEXT NOT NULL
                   CHECK (source IN ('upload', 'image', 'config', 'rollback', 'restart', 'promote')),
    -- For rollback/restart: the release this one was copied from.
    from_release   INTEGER NOT NULL DEFAULT 0,
    image_ref      TEXT NOT NULL DEFAULT '',
    image_id       TEXT NOT NULL DEFAULT '',
    -- Encrypted JSON of the fully resolved container config.
    config         BLOB NOT NULL,
    container_id   TEXT NOT NULL DEFAULT '',
    host_port      INTEGER NOT NULL DEFAULT 0,
    error          TEXT NOT NULL DEFAULT '',
    error_code     TEXT NOT NULL DEFAULT '',
    created_by     TEXT NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at    DATETIME,
    UNIQUE (service_id, number)
);
-- One deploy at a time per service, and at most one active release.
CREATE UNIQUE INDEX releases_one_in_flight ON releases (service_id)
    WHERE status IN ('queued', 'building', 'starting', 'health_checking');
CREATE UNIQUE INDEX releases_one_active ON releases (service_id) WHERE status = 'active';

CREATE TABLE release_events (
    id         INTEGER PRIMARY KEY,
    release_id INTEGER NOT NULL REFERENCES releases (id),
    message    TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX release_events_release ON release_events (release_id);

CREATE TABLE audit_events (
    id             INTEGER PRIMARY KEY,
    actor          TEXT NOT NULL,
    actor_kind     TEXT NOT NULL CHECK (actor_kind IN ('user', 'token', 'system')),
    user_id        INTEGER,
    token_id       INTEGER,
    action         TEXT NOT NULL,
    project_id     INTEGER,
    environment_id INTEGER,
    target         TEXT NOT NULL,
    summary        TEXT NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX audit_events_project ON audit_events (project_id, id);
