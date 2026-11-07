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
SELECT sqlc.embed(cloudflare_zones), CAST(coalesce(cloudflare_connections.org_id, 0) AS INTEGER) AS org_id, cloudflare_connections.shared
FROM cloudflare_zones JOIN cloudflare_connections ON cloudflare_connections.id = cloudflare_zones.connection_id
ORDER BY cloudflare_zones.name;

-- name: DeleteCloudflareZones :exec
DELETE FROM cloudflare_zones WHERE connection_id = +?;

-- name: SaveCloudflareZone :exec
-- A zone another connection reached before moves to this one; core
-- refuses a zone another owner's connection has (saveZones).
INSERT INTO cloudflare_zones (connection_id, zone_id, name) VALUES (?, ?, ?)
ON CONFLICT (name) DO UPDATE SET connection_id = excluded.connection_id, zone_id = excluded.zone_id;

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

-- name: SaveCloudflareRecord :exec
-- Routes hostname through its server's tunnel, pending. A row being
-- deleted (absent) comes back; what it made is kept when the zone is the
-- same.
INSERT INTO cloudflare_records (hostname, domain_id, server_id, connection_id, zone_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (hostname) DO UPDATE SET
    domain_id = excluded.domain_id, server_id = excluded.server_id,
    record_id = CASE WHEN cloudflare_records.zone_id = excluded.zone_id THEN cloudflare_records.record_id ELSE '' END,
    connection_id = excluded.connection_id, zone_id = excluded.zone_id,
    want = 'present', status = 'pending', reason = '',
    attempts = 0, retry_at = NULL, gen = cloudflare_records.gen + 1, updated_at = excluded.updated_at;

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

-- name: CountConnectionUse :one
-- Tunnels Dock keeps with a connection; it can't be removed while any.
SELECT COUNT(*) FROM cloudflare_tunnels WHERE connection_id = +?;
