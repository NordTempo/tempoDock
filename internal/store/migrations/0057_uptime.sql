-- Uptime checks (ADR 0055): a URL on one of a service's domains, probed
-- every interval_s seconds from another server. expect_status is an exact
-- status code, or 2 or 3 for any 2xx or 3xx code (node.ProbeRequest).
CREATE TABLE uptime_checks (
    id            INTEGER PRIMARY KEY,
    service_id    INTEGER NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    url           TEXT NOT NULL,
    expect_status INTEGER NOT NULL,
    interval_s    INTEGER NOT NULL,
    timeout_ms    INTEGER NOT NULL,
    created_at    DATETIME NOT NULL,
    created_by    TEXT NOT NULL
);
CREATE INDEX uptime_checks_service ON uptime_checks (service_id);

-- Probe results per check, like traffic_stats: res is 60 (kept 24 hours)
-- or 3600 (kept 30 days). Each probe adds to its minute's row; the
-- statistics tick rolls finished hours into hour rows. Reads are per
-- check, so the check leads the key; rollups and trims go by time.
CREATE TABLE uptime_stats (
    check_id       INTEGER NOT NULL REFERENCES uptime_checks (id) ON DELETE CASCADE,
    res            INTEGER NOT NULL,
    at             INTEGER NOT NULL,
    -- Probes that were up, and all probes.
    up             INTEGER NOT NULL,
    total          INTEGER NOT NULL,
    -- The probes' latency, summed.
    latency_ms_sum INTEGER NOT NULL,
    PRIMARY KEY (check_id, res, at)
) WITHOUT ROWID;
CREATE INDEX uptime_stats_time ON uptime_stats (res, at);

-- A check's incidents are its alerts, found by key (uptime:<check id>).
CREATE INDEX alerts_key ON alerts (key, id);
