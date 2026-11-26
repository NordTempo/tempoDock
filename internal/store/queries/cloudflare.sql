-- name: ListCloudflareConnections :many
-- Every connection, the platform's and every org's, for the names kept
-- in memory.
SELECT * FROM cloudflare_connections ORDER BY name;

-- name: ListCloudflareConnectionsOf :many
-- An org's connections (org 0: the platform's), by name.
SELECT * FROM cloudflare_connections WHERE coalesce(org_id, 0) = +CAST(sqlc.arg(org) AS INTEGER) ORDER BY name;

-- name: GetCloudflareConnection :one
-- An org's connection (org 0: the platform's) by name.
SELECT * FROM cloudflare_connections WHERE coalesce(org_id, 0) = +CAST(sqlc.arg(org) AS INTEGER) AND name = +sqlc.arg(name);

-- name: GetCloudflareConnectionByID :one
SELECT * FROM cloudflare_connections WHERE id = +?;

-- name: CreateCloudflareConnection :one
INSERT INTO cloudflare_connections (org_id, name, shared, account_id, account_name, token, created_at, updated_at, updated_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: SetCloudflareConnectionShared :exec
-- Whether org domains may use a platform connection's zones.
UPDATE cloudflare_connections SET shared = ?, updated_at = ?, updated_by = ? WHERE id = +?;

-- name: UpdateCloudflareConnection :one
-- A new token (checked): the connection works again.
UPDATE cloudflare_connections
SET account_id = ?, account_name = ?, token = ?, status = 'ok', status_reason = '', updated_at = ?, updated_by = ?
WHERE id = +?
RETURNING *;

-- name: SetCloudflareConnectionStatus :exec
UPDATE cloudflare_connections SET status = ?, status_reason = ? WHERE id = +?;

-- name: DeleteCloudflareConnection :exec
DELETE FROM cloudflare_connections WHERE id = +?;

-- name: ListCloudflareZones :many
-- Every connection's zones with whose they are (org 0: the platform's)
-- and whether orgs may use them; few, kept in memory to match hostnames.
-- A zone on several connections is listed once for each.
SELECT sqlc.embed(cloudflare_zones), CAST(coalesce(cloudflare_connections.org_id, 0) AS INTEGER) AS org_id, cloudflare_connections.shared
FROM cloudflare_zones JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_zones.connection_id
ORDER BY cloudflare_zones.name, cloudflare_zones.connection_id;

-- name: DeleteCloudflareZones :exec
DELETE FROM cloudflare_zones WHERE connection_id = +?;

-- name: SaveCloudflareZone :exec
-- One of a connection's zones; other connections may hold it too.
INSERT INTO cloudflare_zones (connection_id, zone_id, name) VALUES (?, ?, ?);

-- name: ListCloudflareTunnels :many
-- Every tunnel row, with its connection's name; one per tunnel server.
SELECT sqlc.embed(cloudflare_tunnels), cloudflare_connections.name AS connection_name
FROM cloudflare_tunnels JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_tunnels.connection_id
ORDER BY cloudflare_tunnels.id;

-- name: GetCloudflareTunnelByServer :one
SELECT * FROM cloudflare_tunnels WHERE server_id = +?;

-- name: CreateCloudflareTunnel :one
INSERT INTO cloudflare_tunnels (server_id, server_name, connection_id, tunnel_id, token, adopted, status, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateCloudflareTunnel :execrows
-- The worker's result, saved only if nothing else changed the row since
-- it loaded generation gen.
UPDATE cloudflare_tunnels
SET token = ?, status = ?, reason = ?, attempts = ?, retry_at = ?, updated_at = ?
WHERE id = +sqlc.arg(id) AND gen = +sqlc.arg(gen);

-- name: SetCloudflareTunnelID :exec
-- The tunnel the worker made, saved whatever else changed: a removal
-- must find it to delete it.
UPDATE cloudflare_tunnels SET tunnel_id = ? WHERE id = +?;

-- name: SetCloudflareTunnelReason :exec
UPDATE cloudflare_tunnels SET reason = ? WHERE id = +?;

-- name: SetCloudflareTunnelAbsent :exec
-- The server goes: delete what Dock made.
UPDATE cloudflare_tunnels SET want = 'absent', status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE server_id = +?;

-- name: RetryCloudflareTunnel :exec
UPDATE cloudflare_tunnels SET status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE id = +?;

-- name: DeleteCloudflareTunnel :exec
-- A deleted tunnel's row, unless it was asked for again meanwhile.
DELETE FROM cloudflare_tunnels WHERE id = +sqlc.arg(id) AND gen = +sqlc.arg(gen);

-- name: ListDueCloudflareTunnels :many
-- Pending tunnels whose connection works and whose retry is due.
SELECT cloudflare_tunnels.* FROM cloudflare_tunnels
JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_tunnels.connection_id
WHERE cloudflare_tunnels.status = 'pending' AND cloudflare_connections.status = 'ok'
  AND (cloudflare_tunnels.retry_at IS NULL OR cloudflare_tunnels.retry_at <= +sqlc.arg(now))
ORDER BY cloudflare_tunnels.id;

-- name: NextCloudflareTunnelRetry :many
-- The earliest held-back retry of a pending tunnel whose connection works.
SELECT cloudflare_tunnels.retry_at FROM cloudflare_tunnels
JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_tunnels.connection_id
WHERE cloudflare_tunnels.status = 'pending' AND cloudflare_connections.status = 'ok' AND cloudflare_tunnels.retry_at IS NOT NULL
ORDER BY cloudflare_tunnels.retry_at LIMIT 1;

-- name: SaveCloudflareRecord :execrows
-- Routes hostname through its server's tunnel, pending. A row being
-- deleted (absent, or its domain gone) comes back, the domain's alone
-- (a status page's it was is dropped); what it made is kept when the
-- zone is the same. A live domain's row stays with its
-- connection: another connection's save affects no row, so one org's
-- connection never takes over another's record.
INSERT INTO cloudflare_records (hostname, domain_id, server_id, connection_id, zone_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (hostname) DO UPDATE SET
    domain_id = excluded.domain_id, status_page_id = NULL, server_id = excluded.server_id,
    record_id = CASE WHEN cloudflare_records.zone_id = excluded.zone_id THEN cloudflare_records.record_id ELSE '' END,
    connection_id = excluded.connection_id, zone_id = excluded.zone_id,
    want = 'present', status = 'pending', reason = '',
    attempts = 0, retry_at = NULL, gen = cloudflare_records.gen + 1, updated_at = excluded.updated_at
WHERE cloudflare_records.connection_id = excluded.connection_id
   OR cloudflare_records.want = 'absent' OR (cloudflare_records.domain_id IS NULL AND cloudflare_records.status_page_id IS NULL);

-- name: SaveStatusPageRecord :execrows
-- Routes a status page's hostname through the control plane's tunnel,
-- pending, as SaveCloudflareRecord does a domain's. A live record of
-- anything else is never taken over.
INSERT INTO cloudflare_records (hostname, status_page_id, server_id, connection_id, zone_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (hostname) DO UPDATE SET
    status_page_id = excluded.status_page_id, domain_id = NULL, server_id = excluded.server_id,
    record_id = CASE WHEN cloudflare_records.zone_id = excluded.zone_id THEN cloudflare_records.record_id ELSE '' END,
    connection_id = excluded.connection_id, zone_id = excluded.zone_id,
    want = 'present', status = 'pending', reason = '',
    attempts = 0, retry_at = NULL, gen = cloudflare_records.gen + 1, updated_at = excluded.updated_at
WHERE cloudflare_records.want = 'absent' OR cloudflare_records.status_page_id = excluded.status_page_id
   OR (cloudflare_records.domain_id IS NULL AND cloudflare_records.status_page_id IS NULL);

-- name: SetStatusPageRecordAbsent :exec
-- The page goes: delete the records Dock made for its addresses.
UPDATE cloudflare_records SET want = 'absent', status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE status_page_id = +?;

-- name: SetStatusHostRecordAbsent :exec
-- One of a page's addresses goes (its custom hostname, an old name's
-- address): delete the record Dock made for it.
UPDATE cloudflare_records SET want = 'absent', status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE hostname = +? AND status_page_id = +?;

-- name: GetStatusPageRecord :one
-- The record of one of a page's addresses (its custom hostname).
SELECT * FROM cloudflare_records WHERE hostname = +? AND status_page_id = +? AND want = 'present';

-- name: ListStatusPageRecords :many
-- The addresses Dock routes through the control plane's tunnel for
-- status pages, for syncStatusRecords.
SELECT hostname, status_page_id FROM cloudflare_records WHERE status_page_id IS NOT NULL AND want = 'present';

-- name: GetCloudflareRecord :one
SELECT * FROM cloudflare_records WHERE hostname = +?;

-- name: ListDueCloudflareRecords :many
-- Pending records whose connection works and whose retry is due. The sort
-- is over pending rows only, a few at a time.
SELECT cloudflare_records.* FROM cloudflare_records
JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_records.connection_id
WHERE cloudflare_records.status = 'pending' AND cloudflare_connections.status = 'ok'
  AND (cloudflare_records.retry_at IS NULL OR cloudflare_records.retry_at <= +sqlc.arg(now))
ORDER BY cloudflare_records.id
LIMIT +sqlc.arg(max);

-- name: NextCloudflareRecordRetry :many
-- The earliest held-back retry of a pending record whose connection works.
SELECT cloudflare_records.retry_at FROM cloudflare_records
JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_records.connection_id
WHERE cloudflare_records.status = 'pending' AND cloudflare_connections.status = 'ok' AND cloudflare_records.retry_at IS NOT NULL
ORDER BY cloudflare_records.retry_at LIMIT 1;

-- name: UpdateCloudflareRecord :execrows
-- What the worker found or did, saved only if nothing else changed the
-- row since it loaded generation gen.
UPDATE cloudflare_records
SET record_id = ?, content = ?, route_tunnel = ?, status = ?, reason = ?, attempts = ?, retry_at = ?, updated_at = ?
WHERE id = +sqlc.arg(id) AND gen = +sqlc.arg(gen);

-- name: DeleteCloudflareRecord :exec
-- A deleted record's row, unless it was asked for again meanwhile.
DELETE FROM cloudflare_records WHERE id = +sqlc.arg(id) AND gen = +sqlc.arg(gen);

-- name: RetryCloudflareRecord :exec
-- Works on a record again.
UPDATE cloudflare_records SET status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE id = +?;

-- name: RetryConnectionRecords :exec
-- Works on a connection's failed records again, after it got a new token
-- or was refreshed.
UPDATE cloudflare_records SET status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE connection_id = +? AND status = 'error';

-- name: RetryFailedCloudflare :execrows
UPDATE cloudflare_records SET status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE status = 'error';

-- name: RetryFailedCloudflareTunnels :execrows
UPDATE cloudflare_tunnels SET status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE status = 'error';

-- name: SetDomainRecordAbsent :exec
-- The domain goes: delete the record Dock made for it.
UPDATE cloudflare_records SET want = 'absent', status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE domain_id = +?;

-- name: SetServiceRecordsAbsent :exec
-- The service is archived: delete the records Dock made for its domains.
UPDATE cloudflare_records SET want = 'absent', status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE domain_id IN (SELECT id FROM domains WHERE service_id = +?);

-- name: ListServerCloudflareRecords :many
-- The domains Dock routes through a server's tunnel.
SELECT * FROM cloudflare_records WHERE server_id = +? AND want = 'present';

-- name: ListServiceCloudflareRecords :many
-- The records of a service's domains, for its domain list.
SELECT * FROM cloudflare_records WHERE domain_id IN (SELECT id FROM domains WHERE service_id = +?);

-- name: ListCloudflareRecords :many
-- Every record row for the console, failed ones first, with the
-- server's name (empty once removed).
SELECT sqlc.embed(cloudflare_records), CAST(COALESCE(servers.name, '') AS TEXT) AS server_name, cloudflare_connections.name AS connection_name
FROM cloudflare_records
JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_records.connection_id
LEFT JOIN servers ON servers.id = cloudflare_records.server_id
ORDER BY CASE cloudflare_records.status WHEN 'error' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END, cloudflare_records.hostname
LIMIT +sqlc.arg(max);

-- name: ListStrandedCloudflareRecords :many
-- Records whose connection no longer holds their zone (its token lost
-- it, or the connection is being removed), with the org they serve: the
-- domain's, or once it is gone the connection's (0: the platform's).
-- Every record is looked at; there are as many as domains through
-- tunnels, and this runs only when a connection changes.
SELECT sqlc.embed(cloudflare_records),
    CAST(coalesce(projects.org_id, cloudflare_connections.org_id, 0) AS INTEGER) AS org_id
FROM cloudflare_records
JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_records.connection_id
LEFT JOIN domains ON domains.id = cloudflare_records.domain_id
LEFT JOIN services ON services.id = domains.service_id
LEFT JOIN environments ON environments.id = services.environment_id
LEFT JOIN projects ON projects.id = environments.project_id
WHERE NOT EXISTS (
    SELECT 1 FROM cloudflare_zones
    WHERE cloudflare_zones.connection_id = cloudflare_records.connection_id AND cloudflare_zones.zone_id = cloudflare_records.zone_id
)
ORDER BY cloudflare_records.id;

-- name: MoveCloudflareRecord :exec
-- A record goes to another connection that holds its zone, to be worked
-- on again there: the record Dock made is found again by its comment.
UPDATE cloudflare_records
SET connection_id = ?, status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE id = +?;

-- name: FailCloudflareRecord :exec
-- A record no connection may work on: failed, saying what to do.
UPDATE cloudflare_records
SET status = 'error', reason = ?, attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE id = +?;

-- name: CountConnectionUse :one
-- Tunnels Dock keeps with a connection; it can't be removed while any.
SELECT COUNT(*) FROM cloudflare_tunnels WHERE connection_id = +?;

-- name: ListCloudflareRedirectsOf :many
-- An org's redirects (org 0: the platform's): those on its connections,
-- by hostname, with the connection's name; at most max.
SELECT sqlc.embed(cloudflare_redirects), cloudflare_connections.name AS connection_name
FROM cloudflare_connections JOIN cloudflare_redirects ON cloudflare_redirects.connection_id = cloudflare_connections.id
WHERE coalesce(cloudflare_connections.org_id, 0) = +CAST(sqlc.arg(org) AS INTEGER)
ORDER BY cloudflare_redirects.hostname
LIMIT +sqlc.arg(max);

-- name: GetCloudflareRedirectOf :one
-- One of an org's redirects (org 0: the platform's) by hostname.
SELECT sqlc.embed(cloudflare_redirects), cloudflare_connections.name AS connection_name
FROM cloudflare_redirects JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_redirects.connection_id
WHERE cloudflare_redirects.hostname = +sqlc.arg(hostname) AND coalesce(cloudflare_connections.org_id, 0) = +CAST(sqlc.arg(org) AS INTEGER);

-- name: GetCloudflareRedirectByID :one
-- A redirect with its connection's name and owner (0: the platform), for
-- its live event.
SELECT sqlc.embed(cloudflare_redirects), cloudflare_connections.name AS connection_name,
    CAST(coalesce(cloudflare_connections.org_id, 0) AS INTEGER) AS org_id
FROM cloudflare_redirects JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_redirects.connection_id
WHERE cloudflare_redirects.id = +?;

-- name: RedirectHostnameExists :one
-- Whether a redirect holds a hostname, which then can't be a domain.
SELECT EXISTS (SELECT 1 FROM cloudflare_redirects WHERE hostname = +?);

-- name: CreateCloudflareRedirect :one
INSERT INTO cloudflare_redirects (hostname, connection_id, zone_id, target, status_code, keep_path, created_at, updated_at, updated_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateCloudflareRedirectSettings :one
-- A redirect's new target or kind, to be made in Cloudflare again.
UPDATE cloudflare_redirects
SET target = ?, status_code = ?, keep_path = ?, status = 'pending', reason = '', attempts = 0, retry_at = NULL,
    gen = gen + 1, updated_at = ?, updated_by = ?
WHERE id = +sqlc.arg(id) AND want = 'present'
RETURNING *;

-- name: SetCloudflareRedirectAbsent :exec
-- The redirect goes: delete what Dock made.
UPDATE cloudflare_redirects
SET want = 'absent', status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?, updated_by = ?
WHERE id = +?;

-- name: RetryCloudflareRedirect :exec
UPDATE cloudflare_redirects SET status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE id = +?;

-- name: ListDueCloudflareRedirects :many
-- Pending redirects whose connection works and whose retry is due.
SELECT cloudflare_redirects.* FROM cloudflare_redirects
JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_redirects.connection_id
WHERE cloudflare_redirects.status = 'pending' AND cloudflare_connections.status = 'ok'
  AND (cloudflare_redirects.retry_at IS NULL OR cloudflare_redirects.retry_at <= +sqlc.arg(now))
ORDER BY cloudflare_redirects.id
LIMIT +sqlc.arg(max);

-- name: NextCloudflareRedirectRetry :many
-- The earliest held-back retry of a pending redirect whose connection works.
SELECT cloudflare_redirects.retry_at FROM cloudflare_redirects
JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_redirects.connection_id
WHERE cloudflare_redirects.status = 'pending' AND cloudflare_connections.status = 'ok' AND cloudflare_redirects.retry_at IS NOT NULL
ORDER BY cloudflare_redirects.retry_at LIMIT 1;

-- name: SaveCloudflareRedirectResult :execrows
-- What the worker found or did, saved only if nothing else changed the
-- row since it loaded generation gen.
UPDATE cloudflare_redirects
SET record_id = ?, ruleset_id = ?, rule_id = ?, status = ?, reason = ?, attempts = ?, retry_at = ?, updated_at = ?
WHERE id = +sqlc.arg(id) AND gen = +sqlc.arg(gen);

-- name: DeleteCloudflareRedirect :exec
-- A deleted redirect's row, unless it changed meanwhile.
DELETE FROM cloudflare_redirects WHERE id = +sqlc.arg(id) AND gen = +sqlc.arg(gen);

-- name: ListConnectionRedirects :many
-- A connection's redirects, by hostname: removing it lists or deletes them.
SELECT * FROM cloudflare_redirects WHERE connection_id = +? ORDER BY hostname;

-- name: RetryConnectionRedirects :exec
-- Works on a connection's failed redirects again, after it got a new
-- token or was refreshed.
UPDATE cloudflare_redirects SET status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE connection_id = +? AND status = 'error';

-- name: RetryFailedCloudflareRedirects :execrows
UPDATE cloudflare_redirects SET status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE status = 'error';

-- name: FailStrandedCloudflareRedirects :many
-- A connection's redirects in zones it no longer reaches: failed, saying
-- what to do. Returns their IDs, for live events.
UPDATE cloudflare_redirects
SET status = 'error', reason = ?, attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?
WHERE cloudflare_redirects.connection_id = +sqlc.arg(connection) AND cloudflare_redirects.want = 'present' AND NOT EXISTS (
    SELECT 1 FROM cloudflare_zones
    WHERE cloudflare_zones.connection_id = cloudflare_redirects.connection_id AND cloudflare_zones.zone_id = cloudflare_redirects.zone_id
)
RETURNING id;

-- name: SetConnectionRedirectsAbsent :exec
-- A connection is removed with its redirects: delete what Dock made.
UPDATE cloudflare_redirects
SET want = 'absent', status = 'pending', reason = '', attempts = 0, retry_at = NULL, gen = gen + 1, updated_at = ?, updated_by = ?
WHERE connection_id = +? AND want = 'present';

-- name: ListUnroutedGeneratedDomains :many
-- The generated domains of a server's services that Dock routes through
-- no Cloudflare record (none, or one being deleted): the ones a server
-- that turned into a tunnel server gives records to its tunnel.
SELECT domains.id, domains.hostname
FROM domains
JOIN services ON services.id = domains.service_id
JOIN environments ON environments.id = services.environment_id
LEFT JOIN cloudflare_records ON cloudflare_records.hostname = domains.hostname AND cloudflare_records.want = 'present'
WHERE environments.server_id = +? AND domains.generated AND services.archived_at IS NULL
  AND cloudflare_records.id IS NULL;
