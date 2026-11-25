-- foreign_keys: off
-- Alert channels gain two kinds (ADR 0020): discord, a Discord webhook,
-- and pushover, a Pushover user or group key with an application token.
-- Rebuilt to widen the CHECK on kind; alert_notices and
-- alert_channel_copies reference it by id, which stays.
CREATE TABLE alert_channels_new (
    id               INTEGER PRIMARY KEY,
    org_id           INTEGER REFERENCES orgs (id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    kind             TEXT NOT NULL CHECK (kind IN ('webhook', 'slack', 'discord', 'pushover', 'email')),
    target           TEXT NOT NULL,
    config           BLOB NOT NULL,
    min_severity     TEXT NOT NULL CHECK (min_severity IN ('warning', 'critical')),
    renotify_minutes INTEGER NOT NULL DEFAULT 0,
    created_at       DATETIME NOT NULL,
    last_sent_at     DATETIME,
    last_error       TEXT NOT NULL DEFAULT '',
    last_error_at    DATETIME,
    failures         INTEGER NOT NULL DEFAULT 0
);
INSERT INTO alert_channels_new (id, org_id, name, kind, target, config, min_severity, renotify_minutes, created_at, last_sent_at, last_error, last_error_at, failures)
SELECT id, org_id, name, kind, target, config, min_severity, renotify_minutes, created_at, last_sent_at, last_error, last_error_at, failures
FROM alert_channels;
DROP TABLE alert_channels;
ALTER TABLE alert_channels_new RENAME TO alert_channels;
CREATE UNIQUE INDEX alert_channels_name ON alert_channels (coalesce(org_id, 0), name);
