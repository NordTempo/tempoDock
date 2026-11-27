-- Host checks (ADR 0109): read-only security checks of each server's host.
--
-- host_checks is each server's last check: when it ran, its results as
-- JSON (apitypes.HostCheck, each with when its finding was first seen),
-- and why it failed when it did (the results are then the check's
-- before). One row per server; it goes with the server.
CREATE TABLE host_checks (
    server_id  INTEGER PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,
    checked_at TIMESTAMP NOT NULL,
    results    TEXT NOT NULL DEFAULT '[]',
    error      TEXT NOT NULL DEFAULT ''
);

-- host_check_dismissals are findings an operator dismissed, with why:
-- finding is its key (check, or check:subject). A dismissed finding
-- shows as such and raises no alert until restored, also when it goes
-- away and comes back.
CREATE TABLE host_check_dismissals (
    server_id    INTEGER NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    finding      TEXT NOT NULL,
    reason       TEXT NOT NULL,
    dismissed_by TEXT NOT NULL,
    dismissed_at TIMESTAMP NOT NULL,
    PRIMARY KEY (server_id, finding)
);
