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
