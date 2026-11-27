-- name: ListCustomCertificates :many
-- Every uploaded certificate (bounded by what orgs upload), for loading
-- them into memory and the certificate worker's pass.
SELECT * FROM custom_certificates ORDER BY id;

-- name: ListOrgCustomCertificates :many
-- An org's certificates, newest first.
SELECT * FROM custom_certificates WHERE org_id = +? ORDER BY id DESC;

-- name: CountOrgCustomCertificates :one
-- How many certificates an org keeps (at most maxOrgCertificates).
SELECT count(*) FROM custom_certificates WHERE org_id = +?;

-- name: GetCustomCertificate :one
SELECT * FROM custom_certificates WHERE id = +? AND org_id = +?;

-- name: CreateCustomCertificate :one
INSERT INTO custom_certificates (org_id, names, chain, key, fingerprint, issuer, public, not_before, not_after, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: DeleteCustomCertificate :execrows
DELETE FROM custom_certificates WHERE id = +? AND org_id = +?;

-- name: SetCustomCertificateAlerted :execrows
-- The last expiry alert raised for a certificate (0 after it resolved).
UPDATE custom_certificates SET alerted = ? WHERE id = +?;

-- name: DeleteOrgCustomCertificates :exec
-- Part of deleting an org (DeleteOrg).
DELETE FROM custom_certificates WHERE org_id = +?;

-- name: ListOrgVerifiedDomains :many
-- The verified domains of an org's non-archived services, with where each
-- is (and its server): what its uploaded certificates may serve.
-- tunnel_status is the status of the Cloudflare record Dock routes it
-- through a tunnel with ('' for none): such a domain's certificate is
-- Cloudflare's (tunnelOnlyHosts decides from it).
SELECT domains.hostname, projects.name AS project, environments.name AS environment, services.name AS service,
       environments.server_id,
       CAST(coalesce((SELECT cloudflare_records.status FROM cloudflare_records
                      WHERE cloudflare_records.hostname = domains.hostname AND cloudflare_records.want = 'present'), '') AS TEXT) AS tunnel_status
FROM projects
JOIN environments ON environments.project_id = projects.id
JOIN services ON services.environment_id = environments.id
JOIN domains ON domains.service_id = services.id
WHERE projects.org_id = +? AND services.archived_at IS NULL AND domains.verified_at IS NOT NULL;
