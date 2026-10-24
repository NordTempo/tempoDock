-- Traffic statistics (ADR 0033): what Caddy served for each service's
-- domains, counted on its server from the access log and drained by the
-- minute tick. Timestamps are unix seconds. res is 60 (kept 24 hours) or
-- 3600 (kept 30 days); an hour's point is added to on every tick while the
-- hour lasts. Reads are per service, so the service leads the key.
CREATE TABLE traffic_stats (
    service_id  INTEGER NOT NULL,
    res         INTEGER NOT NULL,
    at          INTEGER NOT NULL,
    requests    INTEGER NOT NULL,
    -- Responses by status class; status_2xx counts 1xx too.
    status_2xx  INTEGER NOT NULL,
    status_3xx  INTEGER NOT NULL,
    status_4xx  INTEGER NOT NULL,
    status_5xx  INTEGER NOT NULL,
    bytes       INTEGER NOT NULL,
    -- The time the requests took, summed.
    duration_ms INTEGER NOT NULL,
    -- Distinct visitors in the interval (estimated).
    visitors    INTEGER NOT NULL,
    PRIMARY KEY (service_id, res, at)
) WITHOUT ROWID;
CREATE INDEX traffic_stats_prune ON traffic_stats (res, at);

-- Each hour's visitor sketch (traffic.Sketch) per service, merged across a
-- range for its visitor count. Kept 30 days.
CREATE TABLE traffic_visitors (
    service_id INTEGER NOT NULL,
    at         INTEGER NOT NULL,
    sketch     BLOB NOT NULL,
    PRIMARY KEY (service_id, at)
) WITHOUT ROWID;
CREATE INDEX traffic_visitors_at ON traffic_visitors (at);

-- Requests per method and path (IDs replaced by ":id") per service and
-- hour. Once an hour is over only its 100 most requested paths are kept.
-- Kept 30 days.
CREATE TABLE traffic_paths (
    service_id INTEGER NOT NULL,
    at         INTEGER NOT NULL,
    method     TEXT NOT NULL,
    path       TEXT NOT NULL,
    requests   INTEGER NOT NULL,
    PRIMARY KEY (service_id, at, method, path)
) WITHOUT ROWID;
CREATE INDEX traffic_paths_at ON traffic_paths (at);
