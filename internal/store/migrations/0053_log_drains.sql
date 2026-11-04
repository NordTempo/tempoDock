-- Log drains (ADR 0046): where an environment's app output is sent
-- (an HTTP endpoint taking JSON lines, a syslog server, Loki). config is
-- the sealed settings (URL, headers, token); target is what lists show,
-- without secrets. Delivery counters live in memory only.
CREATE TABLE log_drains (
    id             INTEGER PRIMARY KEY,
    environment_id INTEGER NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN ('http', 'syslog', 'loki')),
    name           TEXT NOT NULL,
    target         TEXT NOT NULL,
    config         BLOB NOT NULL,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMP NOT NULL
);
-- Names are unique per environment; the index also serves the
-- per-environment list.
CREATE UNIQUE INDEX log_drains_name ON log_drains (environment_id, name);
