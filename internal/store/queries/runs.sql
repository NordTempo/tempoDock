-- name: InsertServiceRun :one
INSERT INTO service_runs (service_id, release_id, job_id, trigger, command, tty, volumes, timeout_seconds, status, actor, actor_kind, user_id, token_id, started_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'running', ?, ?, ?, ?, ?)
RETURNING *;

-- name: FinishServiceRun :exec
UPDATE service_runs SET status = ?, exit_code = ?, error = ?, output = ?, finished_at = ? WHERE id = +?;

-- name: GetServiceRun :one
-- A service's run by ID: the service guards IDs from other services.
SELECT * FROM service_runs WHERE id = +? AND service_id = +?;

-- name: ListServiceRuns :many
-- A service's one-off commands newest first (its jobs' runs are their
-- own, ListJobRuns), without their output, with the number of the
-- release each ran in.
SELECT r.id, r.service_id, r.release_id, r.trigger, r.command, r.tty, r.volumes, r.timeout_seconds, r.status,
       r.exit_code, r.error, r.actor, r.actor_kind, r.started_at, r.finished_at, COALESCE(rel.number, 0) AS release_number
FROM service_runs r LEFT JOIN releases rel ON rel.id = r.release_id
WHERE r.service_id = +? AND r.job_id IS NULL ORDER BY r.id DESC LIMIT +?;

-- name: ListRunningServiceRuns :many
-- Runs still marked running: at start, the ones a restart cut short.
SELECT * FROM service_runs WHERE status = 'running';

-- name: TrimServiceRuns :exec
-- Deletes a service's finished one-off commands older than its newest
-- `keep` (jobs keep their own, TrimJobRuns).
DELETE FROM service_runs
WHERE service_runs.service_id = +sqlc.arg(service_id) AND service_runs.job_id IS NULL AND service_runs.status <> 'running' AND service_runs.id < (
    SELECT r.id FROM service_runs r WHERE r.service_id = +sqlc.arg(service_id) AND r.job_id IS NULL
    ORDER BY r.id DESC LIMIT 1 OFFSET CAST(sqlc.arg(keep) AS INTEGER) - 1
);

-- name: InsertSkippedRun :one
-- A scheduled run of a job that didn't start, with why (error).
INSERT INTO service_runs (service_id, release_id, job_id, trigger, command, volumes, timeout_seconds, status, error, actor, actor_kind, started_at, finished_at)
VALUES (?, ?, ?, ?, ?, 'none', ?, 'skipped', ?, ?, ?, ?, ?)
RETURNING *;
