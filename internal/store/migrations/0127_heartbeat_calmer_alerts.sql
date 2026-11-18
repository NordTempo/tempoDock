-- An outside heartbeat and calmer alerts (ADR 0097).
--
-- heartbeat is one row (id 1) while a heartbeat is set: the URL the
-- control plane requests every `minutes` while it runs. url is sealed
-- with the secrets key (it holds the check's ID); target is its scheme
-- and host, which lists show.
CREATE TABLE heartbeat (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    url        BLOB NOT NULL,
    target     TEXT NOT NULL,
    minutes    INTEGER NOT NULL,
    updated_by TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

-- count is how many times an alert's condition occurred while it was
-- open; cleared_at is when the condition of an alert that settles before
-- it resolves last cleared (NULL: it holds).
ALTER TABLE alerts ADD COLUMN count INTEGER NOT NULL DEFAULT 1;
ALTER TABLE alerts ADD COLUMN cleared_at TIMESTAMP;
