-- Dock Analytics (ADR 0037): which server runs the store, the install's
-- token for it, and the daily salts visitor IDs are hashed with.
--
-- analytics_settings is one row (id 1), created when analytics is first
-- used. server_id NULL is the control plane's own server. token is
-- sealed with the secrets key. enabled is set once the store has been
-- started: from then on the control plane keeps it running and forwards
-- every server's spool to it.
CREATE TABLE analytics_settings (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    server_id  INTEGER REFERENCES servers (id),
    token      BLOB NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT 0,
    updated_by TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
-- Removing a server checks it doesn't run the store (the foreign key).
CREATE INDEX analytics_settings_server ON analytics_settings (server_id);

-- A salt per UTC day (days since 1970-01-01), sealed. Only today's and
-- tomorrow's exist: older ones are deleted, so their visitor IDs can't
-- be recomputed.
CREATE TABLE analytics_salts (
    day  INTEGER PRIMARY KEY,
    salt BLOB NOT NULL
);
