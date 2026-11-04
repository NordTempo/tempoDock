-- One-off commands run in a service's active image (`dock run`, ADR
-- 0046), and later its scheduled jobs' runs: a short history per service.
-- Rows are trimmed to the newest few per service as runs finish.
CREATE TABLE service_runs (
    id              INTEGER PRIMARY KEY,
    service_id      INTEGER NOT NULL REFERENCES services (id),
    -- The active release whose image, variables and limits it ran with;
    -- NULL for a scheduled run skipped because there was none.
    release_id      INTEGER REFERENCES releases (id),
    trigger         TEXT NOT NULL CHECK (trigger IN ('command', 'schedule', 'manual')),
    command         TEXT NOT NULL,
    tty             BOOLEAN NOT NULL DEFAULT 0,
    volumes         TEXT NOT NULL CHECK (volumes IN ('none', 'read', 'write')),
    timeout_seconds INTEGER NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'timed_out', 'canceled', 'interrupted', 'skipped')),
    -- NULL when the command never exited by itself (it couldn't start,
    -- timed out or was canceled).
    exit_code       INTEGER,
    error           TEXT NOT NULL DEFAULT '',
    -- The tail of its output; empty for terminal sessions.
    output          TEXT NOT NULL DEFAULT '',
    -- Who ran it, as the audit log names actors.
    actor           TEXT NOT NULL,
    actor_kind      TEXT NOT NULL,
    user_id         INTEGER,
    token_id        INTEGER,
    started_at      TIMESTAMP NOT NULL,
    finished_at     TIMESTAMP
);
-- A service's runs newest first, and trimming them.
CREATE INDEX service_runs_service ON service_runs (service_id, id);
-- Runs a restart interrupted.
CREATE INDEX service_runs_running ON service_runs (status) WHERE status = 'running';
