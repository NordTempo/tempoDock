-- CreateDomain adds a claim on a hostname: verified_at is set for one
-- that needs no TXT record (ADR 0042), else NULL until it verifies.
-- name: CreateDomain :one
INSERT INTO domains (hostname, service_id, generated, redirect_to, verify_token, verified_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING *;

-- GetDomainByHostname returns the domain that holds a hostname: its
-- verified claim, the one that routes (ADR 0042).
-- name: GetDomainByHostname :one
SELECT * FROM domains WHERE hostname = +? AND verified_at IS NOT NULL;

-- GetServiceDomain returns a service's claim on a hostname, verified or
-- not.
-- name: GetServiceDomain :one
SELECT * FROM domains WHERE hostname = +? AND service_id = +?;

-- HostnameExists reports whether any service claims a hostname, verified
-- or not.
-- name: HostnameExists :one
SELECT EXISTS (SELECT 1 FROM domains WHERE hostname = +?);

-- ListHostnameClaims returns every claim on a hostname with the org that
-- made it, for adding and verifying one (ADR 0042).
-- name: ListHostnameClaims :many
SELECT domains.id, domains.service_id, domains.verified_at, projects.org_id
FROM domains
JOIN services ON services.id = domains.service_id
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE domains.hostname = +?
ORDER BY domains.id;

-- VerifyDomain marks a claim verified; it affects no row when it already
-- was. The partial unique index refuses a second verified claim.
-- name: VerifyDomain :execrows
UPDATE domains SET verified_at = ? WHERE id = +? AND verified_at IS NULL;

-- name: ListDomainsByService :many
SELECT * FROM domains WHERE service_id = +? ORDER BY id;

-- name: ListPendingDomains :many
SELECT * FROM domains WHERE status != 'active' ORDER BY id;

-- name: UpdateDomainStatus :exec
UPDATE domains SET status = ?, status_reason = ? WHERE id = +?;

-- name: DeleteDomain :exec
DELETE FROM domains WHERE id = +?;

-- name: ListRoutableDomains :many
-- Every verified domain of a non-archived service in an environment on
-- one server, with the host port and ID of the service's active release
-- (0 if none), the service's access protection (mode '' when public), and
-- its name and whether it is stopped (ADR 0050), and its environment,
-- whether that is asleep and whether it may sleep (ADR 0070).
SELECT domains.hostname, domains.redirect_to, domains.service_id,
       services.name AS service_name,
       CAST(services.stopped_at IS NOT NULL AS BOOLEAN) AS stopped,
       environments.id AS environment_id,
       CAST(environments.asleep_at IS NOT NULL AS BOOLEAN) AS asleep,
       CAST(environments.sleep_after_minutes > 0 OR environments.asleep_at IS NOT NULL AS BOOLEAN) AS sleepable,
       CAST(coalesce((SELECT releases.host_port FROM releases
                      WHERE releases.service_id = services.id AND releases.status = 'active'), 0) AS INTEGER) AS host_port,
       CAST(coalesce((SELECT releases.id FROM releases
                      WHERE releases.service_id = services.id AND releases.status = 'active'), 0) AS INTEGER) AS release_id,
       CAST(coalesce(service_access.mode, '') AS TEXT) AS access_mode,
       CAST(coalesce(service_access.username, '') AS TEXT) AS access_username,
       CAST(coalesce(service_access.password_hash, '') AS TEXT) AS access_password_hash,
       service_access.key AS access_key
FROM domains
JOIN services ON services.id = domains.service_id
JOIN environments ON environments.id = services.environment_id
LEFT JOIN service_access ON service_access.service_id = services.id
WHERE environments.server_id = +? AND services.archived_at IS NULL AND domains.verified_at IS NOT NULL
ORDER BY domains.service_id, domains.id;

-- name: ListProjectDomains :many
-- Every domain of every service in a project.
SELECT domains.* FROM domains
JOIN services ON services.id = domains.service_id
JOIN environments ON environments.id = services.environment_id
WHERE environments.project_id = +?
ORDER BY domains.service_id, domains.id;

-- name: ListEnvironmentDomains :many
-- Every domain of every service in an environment, in no particular order.
SELECT domains.* FROM domains
JOIN services ON services.id = domains.service_id
WHERE services.environment_id = +?;
