-- Services whose active release must be started again once their server
-- is reachable and the interrupted release's container is gone: a
-- stateful recreate (which stops the old release first) cut short by a
-- crash or by its server going offline. Recovery and the reconnect
-- procedure work through it; a row stays until the release is started,
-- so the intent survives daemon restarts.
CREATE TABLE ensure_running (
    service_id INTEGER PRIMARY KEY REFERENCES services (id) ON DELETE CASCADE
);
