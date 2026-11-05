-- Scheduled jobs (ADR 0054): named cron schedules on a service, each
-- running a command from its active image. Their runs are service_runs
-- with the job.
CREATE TABLE service_jobs (
    id              INTEGER PRIMARY KEY,
    service_id      INTEGER NOT NULL REFERENCES services (id),
    name            TEXT NOT NULL,
    schedule        TEXT NOT NULL,
    timezone        TEXT NOT NULL,
    command         TEXT NOT NULL,
    timeout_seconds INTEGER NOT NULL,
    overlap         TEXT NOT NULL CHECK (overlap IN ('skip', 'allow')),
    enabled         BOOLEAN NOT NULL DEFAULT 1,
    -- api: set in the UI, CLI or MCP; repo: from the active release's
    -- dock.toml, changed there.
    source          TEXT NOT NULL CHECK (source IN ('api', 'repo')),
    -- The due time it last ran (or skipped) for; NULL before its first.
    -- Due times are counted from it, or from updated_at, the later.
    last_due_at     TIMESTAMP,
    created_by      TEXT NOT NULL,
    created_at      TIMESTAMP NOT NULL,
    updated_at      TIMESTAMP NOT NULL,
    UNIQUE (service_id, name)
);
-- The scheduler's one list.
CREATE INDEX service_jobs_enabled ON service_jobs (enabled) WHERE enabled = 1;

ALTER TABLE service_runs ADD COLUMN job_id INTEGER REFERENCES service_jobs (id);
-- A job's runs newest first, and trimming them.
CREATE INDEX service_runs_job ON service_runs (job_id, id) WHERE job_id IS NOT NULL;
