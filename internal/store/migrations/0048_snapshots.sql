-- Volume snapshots (ADR 0047): fast copies of a service's volumes into a
-- snapshot volume on the same server, taken by hand or before a risky
-- action, and restored into a new service or in place. A row is a
-- snapshot; its files live in the server's snapshot volume under its ID.
-- Deleting a snapshot removes its files and then its row.
CREATE TABLE volume_snapshots (
    id          INTEGER PRIMARY KEY,
    service_id  INTEGER NOT NULL REFERENCES services (id),
    -- Where the files are: the service's server when it was taken.
    server_id   INTEGER NOT NULL REFERENCES servers (id),
    trigger     TEXT NOT NULL
                CHECK (trigger IN ('manual', 'before_restore', 'before_deploy', 'before_run', 'before_move')),
    status      TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'ready', 'failed')),
    -- The volumes copied, by their name on the service, space separated.
    volumes     TEXT NOT NULL,
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    -- How long the service's containers were paused for the copy.
    paused_ms   INTEGER NOT NULL DEFAULT 0,
    note        TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL,
    started_at  TIMESTAMP NOT NULL,
    finished_at TIMESTAMP
);
CREATE INDEX volume_snapshots_service ON volume_snapshots (service_id, id);
CREATE INDEX volume_snapshots_running ON volume_snapshots (status) WHERE status = 'running';
CREATE INDEX volume_snapshots_started ON volume_snapshots (started_at);
CREATE INDEX volume_snapshots_server ON volume_snapshots (server_id);

-- Restores of a snapshot: into a new service (to_service_id is the new
-- one) or in place (the snapshot's own service). An in-place restore that
-- a restart cut short leaves its service stopped; recovery fails the row
-- and says so.
CREATE TABLE volume_snapshot_restores (
    id            INTEGER PRIMARY KEY,
    snapshot_id   INTEGER REFERENCES volume_snapshots (id) ON DELETE SET NULL,
    service_id    INTEGER NOT NULL REFERENCES services (id),
    to_service_id INTEGER NOT NULL REFERENCES services (id),
    in_place      BOOLEAN NOT NULL,
    -- The snapshot an in-place restore took of the data it replaced.
    safety_id     INTEGER REFERENCES volume_snapshots (id) ON DELETE SET NULL,
    status        TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'succeeded', 'failed')),
    error         TEXT NOT NULL DEFAULT '',
    created_by    TEXT NOT NULL,
    started_at    TIMESTAMP NOT NULL,
    finished_at   TIMESTAMP
);
CREATE INDEX volume_snapshot_restores_service ON volume_snapshot_restores (service_id, id);
CREATE INDEX volume_snapshot_restores_snapshot ON volume_snapshot_restores (snapshot_id);
CREATE INDEX volume_snapshot_restores_safety ON volume_snapshot_restores (safety_id);
CREATE INDEX volume_snapshot_restores_to ON volume_snapshot_restores (to_service_id);
CREATE INDEX volume_snapshot_restores_running ON volume_snapshot_restores (status) WHERE status = 'running';
