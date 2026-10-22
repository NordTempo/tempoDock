-- Alerts (ADR 0020). One row per occurrence of a problem: open until its
-- condition clears (or an admin resolves it), then kept for [retention]
-- alert_days. key names the condition (disk:<server>, deploy:<service>,
-- ...): at most one alert per key is open, so a condition seen again
-- updates its open alert instead of raising another. Names are copied in,
-- so resolved alerts still read right after what they name is gone.
CREATE TABLE alerts (
    id          INTEGER PRIMARY KEY,
    key         TEXT NOT NULL,
    kind        TEXT NOT NULL,
    severity    TEXT NOT NULL CHECK (severity IN ('warning', 'critical')),
    title       TEXT NOT NULL,
    message     TEXT NOT NULL DEFAULT '',
    project     TEXT NOT NULL DEFAULT '',
    environment TEXT NOT NULL DEFAULT '',
    service     TEXT NOT NULL DEFAULT '',
    server      TEXT NOT NULL DEFAULT '',
    opened_at   DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL,
    resolved_at DATETIME
);
CREATE UNIQUE INDEX alerts_open_key ON alerts (key) WHERE resolved_at IS NULL;
-- Retention deletes resolved alerts by age.
CREATE INDEX alerts_resolved ON alerts (resolved_at) WHERE resolved_at IS NOT NULL;

-- Where alerts are sent. config is sealed JSON (secrets.Box) holding the
-- URL, the webhook signing secret and SMTP settings: never returned by the
-- API. target is what lists show instead (a URL's host, email recipients).
CREATE TABLE alert_channels (
    id               INTEGER PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    kind             TEXT NOT NULL CHECK (kind IN ('webhook', 'slack', 'email')),
    target           TEXT NOT NULL,
    config           BLOB NOT NULL,
    min_severity     TEXT NOT NULL CHECK (min_severity IN ('warning', 'critical')),
    renotify_minutes INTEGER NOT NULL DEFAULT 0,
    created_at       DATETIME NOT NULL,
    last_sent_at     DATETIME,
    last_error       TEXT NOT NULL DEFAULT '',
    last_error_at    DATETIME,
    -- Deliveries that failed (after retries) since the last one that worked.
    failures         INTEGER NOT NULL DEFAULT 0
);

-- Which channels were told about an open alert, and when last (unix
-- seconds): reminders go out renotify_minutes after it, and the alert's
-- resolution goes to exactly these channels.
CREATE TABLE alert_notices (
    alert_id    INTEGER NOT NULL REFERENCES alerts (id) ON DELETE CASCADE,
    channel_id  INTEGER NOT NULL REFERENCES alert_channels (id) ON DELETE CASCADE,
    notified_at INTEGER NOT NULL,
    PRIMARY KEY (alert_id, channel_id)
) WITHOUT ROWID;
CREATE INDEX alert_notices_channel ON alert_notices (channel_id);

-- Release history retention: once a release's build log, saved container
-- output and progress events are gone, it is marked so retention never
-- looks at it again. The partial index holds only releases not yet
-- trimmed: the rollback windows, recent releases and active ones.
ALTER TABLE releases ADD COLUMN history_trimmed BOOLEAN NOT NULL DEFAULT 0;
CREATE INDEX releases_history ON releases (id) WHERE history_trimmed = 0;
