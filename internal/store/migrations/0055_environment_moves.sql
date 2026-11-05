-- Moving an environment to another server (ADR 0053). A move is a state
-- machine recorded step by step, so a restarted daemon finishes it: one
-- that hadn't switched yet rolls back, one that had cleans up.
CREATE TABLE environment_moves (
    id             INTEGER PRIMARY KEY,
    environment_id INTEGER NOT NULL REFERENCES environments (id),
    from_server_id INTEGER NOT NULL REFERENCES servers (id),
    to_server_id   INTEGER NOT NULL REFERENCES servers (id),
    status         TEXT NOT NULL DEFAULT 'running'
                   CHECK (status IN ('running', 'rolling_back', 'succeeded', 'failed')),
    step           TEXT NOT NULL DEFAULT 'prepare'
                   CHECK (step IN ('prepare', 'stop', 'copy', 'start', 'switch', 'cleanup', 'done')),
    -- The downtime of its stateful services: from their stop on the
    -- source to their health check passing on the target.
    downtime_from  TIMESTAMP,
    downtime_to    TIMESTAMP,
    -- What it couldn't do or clean up, and DNS left to change, as text.
    error          TEXT NOT NULL DEFAULT '',
    notes          TEXT NOT NULL DEFAULT '',
    created_by     TEXT NOT NULL,
    started_at     TIMESTAMP NOT NULL,
    finished_at    TIMESTAMP
);
-- One move of an environment at a time.
CREATE UNIQUE INDEX environment_moves_open ON environment_moves (environment_id)
    WHERE status IN ('running', 'rolling_back');
CREATE INDEX environment_moves_env ON environment_moves (environment_id, id);
CREATE INDEX environment_moves_from ON environment_moves (from_server_id);
CREATE INDEX environment_moves_to ON environment_moves (to_server_id);

-- Each service a move starts on its target: its active release, the
-- host port reserved for it there, and how far it got.
CREATE TABLE environment_move_services (
    move_id      INTEGER NOT NULL REFERENCES environment_moves (id),
    service_id   INTEGER NOT NULL REFERENCES services (id),
    -- NULL for a service with nothing deployed: only its data moves.
    release_id   INTEGER REFERENCES releases (id),
    stateful     BOOLEAN NOT NULL,
    host_port    INTEGER NOT NULL DEFAULT 0,
    container_id TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'waiting'
                 CHECK (status IN ('waiting', 'stopped', 'copied', 'started', 'healthy', 'failed')),
    copied_bytes INTEGER NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (move_id, service_id)
) WITHOUT ROWID;
CREATE INDEX environment_move_services_service ON environment_move_services (service_id);
CREATE INDEX environment_move_services_release ON environment_move_services (release_id);
