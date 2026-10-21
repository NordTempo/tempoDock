-- name: CreateDomain :one
INSERT INTO domains (hostname, service_id, generated, redirect_to) VALUES (?, ?, ?, ?) RETURNING *;

-- name: GetDomainByHostname :one
SELECT * FROM domains WHERE hostname = ?;

-- name: HostnameExists :one
SELECT EXISTS (SELECT 1 FROM domains WHERE hostname = ?);

-- name: ListDomainsByService :many
SELECT * FROM domains WHERE service_id = ? ORDER BY id;

-- name: ListPendingDomains :many
SELECT * FROM domains WHERE status != 'active' ORDER BY id;

-- name: UpdateDomainStatus :exec
UPDATE domains SET status = ?, status_reason = ? WHERE id = ?;

-- name: DeleteDomain :exec
DELETE FROM domains WHERE id = ?;

-- name: ListRoutableDomains :many
-- Every domain of a non-archived service in an environment on one server,
-- with the host port of the service's active release (0 if none).
SELECT domains.hostname, domains.redirect_to, domains.service_id,
       CAST(coalesce((SELECT releases.host_port FROM releases
                      WHERE releases.service_id = services.id AND releases.status = 'active'), 0) AS INTEGER) AS host_port
FROM domains
JOIN services ON services.id = domains.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.server_id = ? AND services.archived_at IS NULL
ORDER BY domains.service_id, domains.id;

-- name: ListProjectDomains :many
-- Every domain of every service in a project.
SELECT domains.* FROM domains
JOIN services ON services.id = domains.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.project_id = ?
ORDER BY domains.service_id, domains.id;
