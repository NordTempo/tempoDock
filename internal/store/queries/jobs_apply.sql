-- name: ListEnvironmentJobs :many
-- An environment's jobs, by service and name: what a project file's plan
-- compares (ADR 0049, 0052).
SELECT service_jobs.* FROM service_jobs
JOIN services ON services.id = service_jobs.service_id
WHERE services.environment_id = +?
ORDER BY service_jobs.service_id, service_jobs.name;

-- name: ListProjectAPIJobs :many
-- A project's jobs that aren't from a dock.toml, by service and name: what
-- `dock project export` writes.
SELECT service_jobs.* FROM service_jobs
JOIN services ON services.id = service_jobs.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.project_id = +? AND service_jobs.source = 'api'
ORDER BY service_jobs.service_id, service_jobs.name;
