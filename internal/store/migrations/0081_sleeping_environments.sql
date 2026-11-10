-- Sleeping environments (ADR 0070): an environment without
-- requests for sleep_after_minutes stops its services' containers (kept,
-- not removed) and wakes on the next request to its domains. asleep_at is
-- the target state: set before anything stops, cleared once every service
-- passed its health check again, so recovery converges to it after a
-- crash. Previews get the project's default from preview_settings; open
-- ones get it now. Production environments never sleep (core refuses).
ALTER TABLE environments ADD COLUMN sleep_after_minutes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE environments ADD COLUMN asleep_at DATETIME;
-- slept_by is '' for an idle sleep, else who put it to sleep.
ALTER TABLE environments ADD COLUMN slept_by TEXT NOT NULL DEFAULT '';
-- freed_bytes is the memory its containers used when it went to sleep.
ALTER TABLE environments ADD COLUMN freed_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE environments ADD COLUMN woke_at DATETIME;
-- woke_by is who woke it, 'deploy', or the request that did ('GET /path').
ALTER TABLE environments ADD COLUMN woke_by TEXT NOT NULL DEFAULT '';

ALTER TABLE preview_settings ADD COLUMN sleep_after_minutes INTEGER NOT NULL DEFAULT 30;
UPDATE environments SET sleep_after_minutes = 30
WHERE archived_at IS NULL AND id IN (SELECT environment_id FROM previews WHERE closed_at IS NULL);

-- The sleep manager's one load: environments that may sleep or are asleep.
CREATE INDEX environments_sleepable ON environments (id)
WHERE sleep_after_minutes > 0 OR asleep_at IS NOT NULL;
