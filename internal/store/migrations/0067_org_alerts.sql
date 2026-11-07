-- foreign_keys: off
-- Alerts belong to an org or to the platform (ADR 0062). An alert about a
-- project or service is its org's (org_id); one about a server or Dock
-- itself has none and is the platform's. Existing alerts get the org of
-- the project or service they name.
ALTER TABLE alerts ADD COLUMN org_id INTEGER REFERENCES orgs (id) ON DELETE CASCADE;
UPDATE alerts SET org_id = (SELECT org_id FROM projects WHERE projects.id = alerts.project_id)
WHERE project_id IS NOT NULL;
UPDATE alerts SET org_id = (
    SELECT projects.org_id FROM services
    JOIN environments ON environments.id = services.environment_id
    JOIN projects ON projects.id = environments.project_id
    WHERE services.id = alerts.service_id)
WHERE org_id IS NULL AND service_id IS NOT NULL;
-- An org's (or with 0 the platform's) history, newest first.
CREATE INDEX alerts_org ON alerts (coalesce(org_id, 0), id);

-- Channels belong to an org (its alerts) or to the platform (org_id NULL,
-- the platform's alerts). Names are unique per org. Rebuilt to drop the
-- install-wide UNIQUE on name.
CREATE TABLE alert_channels_new (
    id               INTEGER PRIMARY KEY,
    org_id           INTEGER REFERENCES orgs (id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    kind             TEXT NOT NULL CHECK (kind IN ('webhook', 'slack', 'email')),
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
INSERT INTO alert_channels_new (id, name, kind, target, config, min_severity, renotify_minutes, created_at, last_sent_at, last_error, last_error_at, failures)
SELECT id, name, kind, target, config, min_severity, renotify_minutes, created_at, last_sent_at, last_error, last_error_at, failures
FROM alert_channels;
DROP TABLE alert_channels;
ALTER TABLE alert_channels_new RENAME TO alert_channels;
CREATE UNIQUE INDEX alert_channels_name ON alert_channels (coalesce(org_id, 0), name);

-- Platform channels received every alert until now. So that the orgs the
-- operators run keep hearing about their services, each platform channel
-- is copied into every org with an operator among its admins. A copy's
-- settings are sealed for its own row, so the daemon makes the copies on
-- start (copyPlatformChannels) and deletes each row here as it does.
CREATE TABLE alert_channel_copies (
    channel_id INTEGER NOT NULL REFERENCES alert_channels (id) ON DELETE CASCADE,
    org_id     INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    PRIMARY KEY (channel_id, org_id)
) WITHOUT ROWID;
INSERT INTO alert_channel_copies (channel_id, org_id)
SELECT alert_channels.id, org_members.org_id
FROM alert_channels
JOIN org_members ON org_members.role = 'admin'
JOIN users ON users.id = org_members.user_id AND users.operator
GROUP BY alert_channels.id, org_members.org_id;
