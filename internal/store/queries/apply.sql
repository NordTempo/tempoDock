-- name: SetServiceStaged :exec
-- Stages a service's creation or archive ('create', 'archive'), or clears
-- it ('') once applied or discarded (ADR 0049).
UPDATE services SET staged = ? WHERE id = +?;

-- name: DeleteStagedService :exec
-- Only for discarding a staged creation: the service never ran. Its
-- staged variables, volumes and domains are deleted first.
DELETE FROM services WHERE id = +? AND staged = 'create';

-- name: DeleteServiceVariables :exec
-- Only for discarding a staged service's variables.
DELETE FROM variables WHERE environment_id = +sqlc.arg(environment_id)
  AND coalesce(service_id, 0) = +sqlc.arg(service_id);

-- name: DeleteServiceStagedVolumes :exec
DELETE FROM volumes WHERE service_id = +? AND pending = 1;

-- name: InsertStagedDomain :exec
-- Stages a domain change; staging the same hostname again replaces it.
INSERT INTO staged_domains (service_id, hostname, action, generated, redirect_to) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (service_id, hostname) DO UPDATE SET action = excluded.action, generated = excluded.generated,
    redirect_to = excluded.redirect_to;

-- name: ListEnvironmentStagedDomains :many
-- An environment's staged domain changes, by service name and hostname.
SELECT staged_domains.*, services.name AS service_name FROM staged_domains
JOIN services ON services.id = staged_domains.service_id
WHERE services.environment_id = +?
ORDER BY services.name, staged_domains.hostname;

-- name: DeleteStagedDomain :exec
DELETE FROM staged_domains WHERE id = +?;

-- name: DeleteServiceStagedDomains :exec
DELETE FROM staged_domains WHERE service_id = +?;

-- name: ListProjectLiveVariables :many
-- A project's live variables with their sealed values, by environment,
-- scope and key. For plans and the export, which only writes values made
-- of references (ADR 0049).
SELECT variables.environment_id, variables.service_id, variables.key, variables.value FROM variables
JOIN environments ON environments.id = variables.environment_id
WHERE environments.project_id = +? AND variables.value IS NOT NULL
ORDER BY variables.environment_id, coalesce(variables.service_id, 0), variables.key;

-- name: ListEnvironmentGitConnections :many
-- The non-secret settings of an environment's git connections, by
-- service: what a plan compares (ADR 0049).
SELECT git_connections.service_id, git_connections.repo, git_connections.branch, git_connections.dir,
       git_connections.post_status
FROM git_connections
JOIN services ON services.id = git_connections.service_id
WHERE services.environment_id = +?
ORDER BY git_connections.service_id;

-- name: DeleteEnvironmentStagedDomains :exec
-- Discards every staged domain change of an environment.
DELETE FROM staged_domains WHERE service_id IN (SELECT id FROM services WHERE environment_id = +?);
