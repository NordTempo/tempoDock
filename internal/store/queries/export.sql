-- name: ListProjectVariableNames :many
-- The names of a project's live variables, by environment, scope and
-- name. Values are never selected: an export never reads a secret.
SELECT variables.environment_id, variables.service_id, variables.key FROM variables
JOIN environments ON environments.id = variables.environment_id
WHERE environments.project_id = +? AND variables.value IS NOT NULL
ORDER BY variables.environment_id, coalesce(variables.service_id, 0), variables.key;

-- name: ListProjectGitConnections :many
-- The non-secret settings of a project's git connections (no webhook
-- secret, no token), by service.
SELECT git_connections.service_id, git_connections.repo, git_connections.branch, git_connections.dir,
       git_connections.post_status
FROM git_connections
JOIN services ON services.id = git_connections.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.project_id = +?
ORDER BY git_connections.service_id;
