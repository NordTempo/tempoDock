-- name: ListServiceJobs :many
-- A service's jobs by name.
SELECT * FROM service_jobs WHERE service_id = +? ORDER BY name;

-- name: GetServiceJob :one
SELECT * FROM service_jobs WHERE service_id = +? AND name = +?;

-- name: GetServiceJobByID :one
SELECT * FROM service_jobs WHERE id = +?;

-- name: UpsertServiceJob :one
-- Creates or replaces a job by name; a changed schedule counts its due
-- times from now (updated_at), so it doesn't fire for the past.
INSERT INTO service_jobs (service_id, name, schedule, timezone, command, timeout_seconds, overlap, enabled, source, created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (service_id, name) DO UPDATE SET
    schedule = excluded.schedule, timezone = excluded.timezone, command = excluded.command,
    timeout_seconds = excluded.timeout_seconds, overlap = excluded.overlap, enabled = excluded.enabled,
    source = excluded.source, updated_at = excluded.updated_at
RETURNING *;

-- name: SetServiceJobEnabled :exec
UPDATE service_jobs SET enabled = ?, updated_at = ? WHERE id = +?;

-- name: SetServiceJobDue :exec
-- Records the due time a job ran or skipped for.
UPDATE service_jobs SET last_due_at = ? WHERE id = +?;

-- name: DeleteServiceJobRuns :exec
DELETE FROM service_runs WHERE job_id = +?;

-- name: DeleteServiceJob :exec
DELETE FROM service_jobs WHERE id = +?;

-- name: ListSchedulableJobs :many
-- Every enabled job of a live service, with what the scheduler needs to
-- start it: one query per pass, however many jobs.
SELECT j.id, j.service_id, j.name, j.schedule, j.timezone, j.overlap, j.last_due_at, j.updated_at, e.server_id
FROM service_jobs j
JOIN services s ON s.id = j.service_id
JOIN environments e ON e.id = s.environment_id
WHERE j.enabled = 1 AND s.archived_at IS NULL;

-- name: ListJobRuns :many
-- A job's runs newest first, without their output, with the number of
-- the release each ran in.
SELECT r.id, r.service_id, r.release_id, r.trigger, r.command, r.tty, r.volumes, r.timeout_seconds, r.status,
       r.exit_code, r.error, r.cause, r.actor, r.actor_kind, r.started_at, r.finished_at, r.job_id, COALESCE(rel.number, 0) AS release_number
FROM service_runs r LEFT JOIN releases rel ON rel.id = r.release_id
WHERE r.job_id = +? ORDER BY r.id DESC LIMIT +?;

-- name: ListLatestJobRuns :many
-- Each of a service's jobs' latest run, for the jobs list.
SELECT r.id, r.job_id, r.status, r.exit_code, r.started_at, r.finished_at
FROM service_runs r
WHERE r.id IN (SELECT MAX(x.id) FROM service_runs x WHERE x.service_id = +? AND x.job_id IS NOT NULL GROUP BY x.job_id);

-- name: TrimJobRuns :exec
-- Deletes a job's finished runs older than its newest `keep`.
DELETE FROM service_runs
WHERE service_runs.job_id = +sqlc.arg(job_id) AND service_runs.status <> 'running' AND service_runs.id < (
    SELECT r.id FROM service_runs r WHERE r.job_id = +sqlc.arg(job_id)
    ORDER BY r.id DESC LIMIT 1 OFFSET CAST(sqlc.arg(keep) AS INTEGER) - 1
);
