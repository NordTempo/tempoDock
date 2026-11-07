-- foreign_keys: off
-- Moves without downtime, server drains and forwarding (ADR 0063).
-- Rebuilds environment_moves (a 'sync' step: volumes copied in passes
-- while the source serves, and the drain a move belongs to) and
-- environment_move_services (its 'syncing' and 'synced' states and pass
-- counts), so it runs with foreign keys off.

-- A drain moves every environment of a server (or of one kind) to other
-- servers, one at a time. kind is '' for all of them.
CREATE TABLE server_drains (
    id          INTEGER PRIMARY KEY,
    server_id   INTEGER NOT NULL REFERENCES servers (id),
    kind        TEXT NOT NULL DEFAULT '' CHECK (kind IN ('', 'production', 'non_production')),
    status      TEXT NOT NULL DEFAULT 'running'
                CHECK (status IN ('running', 'paused', 'succeeded', 'cancelled')),
    -- Each move's hold budget, in seconds.
    max_hold_s  INTEGER NOT NULL DEFAULT 30,
    error       TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL,
    started_at  TIMESTAMP NOT NULL,
    finished_at TIMESTAMP
);
-- One drain of a server at a time.
CREATE UNIQUE INDEX server_drains_open ON server_drains (server_id) WHERE status IN ('running', 'paused');
CREATE INDEX server_drains_server ON server_drains (server_id, id);
CREATE INDEX server_drains_status ON server_drains (status) WHERE status = 'running';

CREATE TABLE environment_moves_new (
    id             INTEGER PRIMARY KEY,
    environment_id INTEGER NOT NULL REFERENCES environments (id),
    from_server_id INTEGER NOT NULL REFERENCES servers (id),
    to_server_id   INTEGER NOT NULL REFERENCES servers (id),
    status         TEXT NOT NULL DEFAULT 'running'
                   CHECK (status IN ('running', 'rolling_back', 'succeeded', 'failed')),
    step           TEXT NOT NULL DEFAULT 'prepare'
                   CHECK (step IN ('prepare', 'sync', 'stop', 'copy', 'start', 'switch', 'cleanup', 'done')),
    -- The hold: from the source's services stopping until the target's
    -- pass their health check. Requests wait in Caddy meanwhile.
    downtime_from  TIMESTAMP,
    downtime_to    TIMESTAMP,
    -- The longest hold the move may take, in seconds: the live passes
    -- go on until the last one predicts less.
    max_hold_s     INTEGER NOT NULL DEFAULT 30,
    -- The drain the move is part of, if any.
    drain_id       INTEGER REFERENCES server_drains (id),
    error          TEXT NOT NULL DEFAULT '',
    notes          TEXT NOT NULL DEFAULT '',
    created_by     TEXT NOT NULL,
    started_at     TIMESTAMP NOT NULL,
    finished_at    TIMESTAMP
);
INSERT INTO environment_moves_new (id, environment_id, from_server_id, to_server_id, status, step, downtime_from, downtime_to,
                                   error, notes, created_by, started_at, finished_at)
SELECT id, environment_id, from_server_id, to_server_id, status, step, downtime_from, downtime_to,
       error, notes, created_by, started_at, finished_at FROM environment_moves;
DROP TABLE environment_moves;
ALTER TABLE environment_moves_new RENAME TO environment_moves;
CREATE UNIQUE INDEX environment_moves_open ON environment_moves (environment_id)
    WHERE status IN ('running', 'rolling_back');
CREATE INDEX environment_moves_env ON environment_moves (environment_id, id);
CREATE INDEX environment_moves_from ON environment_moves (from_server_id);
CREATE INDEX environment_moves_to ON environment_moves (to_server_id);
CREATE INDEX environment_moves_drain ON environment_moves (drain_id);

CREATE TABLE environment_move_services_new (
    move_id      INTEGER NOT NULL REFERENCES environment_moves (id),
    service_id   INTEGER NOT NULL REFERENCES services (id),
    release_id   INTEGER REFERENCES releases (id),
    stateful     BOOLEAN NOT NULL,
    host_port    INTEGER NOT NULL DEFAULT 0,
    container_id TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'waiting'
                 CHECK (status IN ('waiting', 'syncing', 'synced', 'stopped', 'copied', 'started', 'healthy', 'failed')),
    copied_bytes INTEGER NOT NULL DEFAULT 0,
    -- Live passes so far, and how long the last one took: the final
    -- pass, in the hold, takes about as long.
    passes       INTEGER NOT NULL DEFAULT 0,
    last_pass_ms INTEGER NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (move_id, service_id)
) WITHOUT ROWID;
INSERT INTO environment_move_services_new (move_id, service_id, release_id, stateful, host_port, container_id, status, copied_bytes, error)
SELECT move_id, service_id, release_id, stateful, host_port, container_id, status, copied_bytes, error FROM environment_move_services;
DROP TABLE environment_move_services;
ALTER TABLE environment_move_services_new RENAME TO environment_move_services;
CREATE INDEX environment_move_services_service ON environment_move_services (service_id);
CREATE INDEX environment_move_services_release ON environment_move_services (release_id);

-- The environments a drain moves, in order, each to its target.
CREATE TABLE server_drain_items (
    drain_id       INTEGER NOT NULL REFERENCES server_drains (id),
    environment_id INTEGER NOT NULL REFERENCES environments (id),
    to_server_id   INTEGER NOT NULL REFERENCES servers (id),
    position       INTEGER NOT NULL,
    status         TEXT NOT NULL DEFAULT 'waiting'
                   CHECK (status IN ('waiting', 'moving', 'moved', 'failed', 'skipped')),
    move_id        INTEGER REFERENCES environment_moves (id),
    error          TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (drain_id, environment_id)
) WITHOUT ROWID;
CREATE INDEX server_drain_items_env ON server_drain_items (environment_id);
CREATE INDEX server_drain_items_to ON server_drain_items (to_server_id);
CREATE INDEX server_drain_items_move ON server_drain_items (move_id);

-- A server that a service moved away from keeps its host port and
-- forwards the connections Caddy still gets for its domains (DNS caches,
-- records Dock doesn't manage) to wherever the service is now, until the
-- server is removed or the service comes back.
CREATE TABLE server_forwards (
    server_id  INTEGER NOT NULL REFERENCES servers (id),
    host_port  INTEGER NOT NULL,
    service_id INTEGER NOT NULL REFERENCES services (id),
    -- The move that made it, for the record: no foreign key, since a
    -- removed server takes its moves' history with it.
    move_id    INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL,
    PRIMARY KEY (server_id, host_port)
) WITHOUT ROWID;
CREATE INDEX server_forwards_service ON server_forwards (service_id);
CREATE INDEX server_forwards_move ON server_forwards (move_id);
