-- Remote servers, join tokens and promotion (ADR 0014). Parameters compared
-- to indexed columns are written `= +?` (docs/performance.md, the
-- re-prepare pitfall). Server rows themselves are read with GetServer,
-- GetServerByName, ListServers and ListServerLoads (projects.sql).

-- name: CreateJoinToken :one
-- Records a join token for `dock server add`; the secret itself is only
-- ever shown once, to the admin. cloudflare_id: make the server a tunnel
-- with that connection once it joins (ADR 0036).
INSERT INTO join_tokens (hash, server_name, accepts_production, accepts_non_production, builder, cloudflare_id, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ConsumeJoinToken :one
-- Marks the token with this hash used, if it is unused and not expired at
-- now, and returns it. sql.ErrNoRows: unknown, used or expired. Run it in
-- the transaction that registers the server (RegisterNodeServer, then
-- SetJoinTokenServer), so a failed registration leaves the token unused.
UPDATE join_tokens SET used_at = sqlc.arg(now)
WHERE hash = +sqlc.arg(hash) AND used_at IS NULL AND expires_at > +sqlc.arg(now)
RETURNING *;

-- name: SetJoinTokenServer :exec
-- Records which server a consumed token created or re-keyed.
UPDATE join_tokens SET used_by_server_id = CAST(sqlc.arg(server_id) AS INTEGER) WHERE id = +sqlc.arg(id);

-- name: ListPendingJoinTokens :many
-- Unused, unexpired tokens: servers that are still "joining".
SELECT * FROM join_tokens WHERE used_at IS NULL AND expires_at > +sqlc.arg(now) ORDER BY expires_at;

-- name: DeleteJoinToken :execrows
-- Cancels an unused token.
DELETE FROM join_tokens WHERE id = +sqlc.arg(id) AND used_at IS NULL;

-- name: RegisterNodeServer :one
-- Creates the server row for a node that joined with a token, pinning its
-- key. A revoked remote server of the same name is re-keyed instead and
-- keeps its settings and environments. sql.ErrNoRows: the name belongs to
-- a server that isn't revoked (or the local one).
INSERT INTO servers (name, accepts_production, accepts_non_production, builder, local, status, key_fingerprint, builds_production, scans_production)
VALUES (sqlc.arg(name), sqlc.arg(accepts_production), sqlc.arg(accepts_non_production), sqlc.arg(builder), 0, 'offline', CAST(sqlc.arg(key_fingerprint) AS TEXT),
    sqlc.arg(accepts_production), sqlc.arg(accepts_production))
ON CONFLICT (name) DO UPDATE SET
    key_fingerprint = excluded.key_fingerprint, next_key_fingerprint = NULL, revoked_at = NULL
WHERE servers.revoked_at IS NOT NULL AND servers.local = 0
RETURNING *;

-- name: GetServerByFingerprint :one
-- The server a connecting node's key belongs to, by its current or its
-- next (rotating) key. Runs on every node handshake.
SELECT * FROM servers
WHERE key_fingerprint = +CAST(sqlc.arg(fingerprint) AS TEXT) OR next_key_fingerprint = +CAST(sqlc.arg(fingerprint) AS TEXT);

-- name: SetServerHello :one
-- Records a link that came up and passed hello: the facts the node
-- reported, and now as connected_at and last_seen_at.
UPDATE servers SET
    version = sqlc.arg(version), docker_version = sqlc.arg(docker_version),
    ingress = sqlc.arg(ingress), public_ips = sqlc.arg(public_ips), tunnel_hostname = sqlc.arg(tunnel_hostname),
    memory_bytes = sqlc.arg(memory_bytes), plain_http = sqlc.arg(plain_http),
    connected_at = sqlc.arg(now), last_seen_at = sqlc.arg(now), disconnect_reason = ''
WHERE id = +sqlc.arg(id)
RETURNING *;

-- name: SetServerDisconnected :exec
-- Records a link that went down at now, and why.
UPDATE servers SET last_seen_at = sqlc.arg(now), disconnect_reason = sqlc.arg(reason) WHERE id = +sqlc.arg(id);

-- name: UpdateServerSettings :one
-- The control plane's own settings for a server (`dock server update`):
-- its base domain, whether it builds and for which kinds, which kinds it
-- scans, and whether it serves through its tunnel.
UPDATE servers SET base_domain = sqlc.arg(base_domain), builder = sqlc.arg(builder),
    through_tunnel = sqlc.arg(through_tunnel),
    builds_production = sqlc.arg(builds_production), builds_non_production = sqlc.arg(builds_non_production),
    scans_production = sqlc.arg(scans_production), scans_non_production = sqlc.arg(scans_non_production)
WHERE id = +sqlc.arg(id)
RETURNING *;

-- name: RevokeServer :one
-- Forgets a remote server's keys, so its next handshake fails; it stays
-- (with its environments) until it re-joins or is removed.
UPDATE servers SET key_fingerprint = NULL, next_key_fingerprint = NULL, revoked_at = sqlc.arg(now)
WHERE servers.id = +sqlc.arg(id) AND servers.local = 0
RETURNING *;

-- name: SetServerNextKey :execrows
-- Starts a key rotation: the node's new key is accepted alongside the
-- current one until PromoteServerKey. 0 rows: revoked or local.
UPDATE servers SET next_key_fingerprint = CAST(sqlc.arg(fingerprint) AS TEXT)
WHERE id = +sqlc.arg(id) AND key_fingerprint IS NOT NULL;

-- name: PromoteServerKey :one
-- Finishes a key rotation once the node connected with its next key: that
-- key becomes the only one. sql.ErrNoRows: no such rotation in progress.
UPDATE servers SET key_fingerprint = next_key_fingerprint, next_key_fingerprint = NULL
WHERE id = +sqlc.arg(id) AND next_key_fingerprint = +CAST(sqlc.arg(fingerprint) AS TEXT)
RETURNING *;

-- name: DeleteServerPlacements :exec
-- Drops a server's project placements (pins included), before DeleteServer.
DELETE FROM project_placements WHERE server_id = +sqlc.arg(server_id);

-- name: ClearArchivedHostPorts :exec
-- Frees the host ports of archived projects' (and archived environments')
-- releases on a server being removed (before MoveArchivedEnvironments):
-- their containers, if any are left, are on that server, not where the
-- environments move.
UPDATE releases SET host_port = 0
WHERE host_port > 0 AND service_id IN (
    SELECT services.id FROM services
    JOIN environments ON environments.id = services.environment_id
    JOIN projects ON projects.id = environments.project_id
    WHERE environments.server_id = +sqlc.arg(server_id)
      AND (projects.archived_at IS NOT NULL OR environments.archived_at IS NOT NULL));

-- name: MoveArchivedEnvironments :exec
-- Moves archived projects' environments, and archived environments
-- (closed previews), off a server being removed, to the control plane's
-- own server: nothing of theirs runs anywhere, and an environment always
-- names a server.
UPDATE environments SET server_id = sqlc.arg(local_server_id)
WHERE server_id = +sqlc.arg(server_id) AND (archived_at IS NOT NULL OR EXISTS (
    SELECT 1 FROM projects WHERE projects.id = environments.project_id AND projects.archived_at IS NOT NULL));

-- name: DeleteServer :execrows
-- Removes a remote server with no environments. 0 rows: local, or
-- environments are still placed there (MoveArchivedEnvironments first).
DELETE FROM servers
WHERE servers.id = +sqlc.arg(id) AND servers.local = 0
  AND NOT EXISTS (SELECT 1 FROM environments WHERE environments.server_id = servers.id);

-- name: SetReleaseDigest :exec
-- Records a pushed build: its registry digest and the builder that made it.
UPDATE releases SET image_digest = sqlc.arg(image_digest), built_on_server_id = sqlc.narg(built_on_server_id)
WHERE id = +sqlc.arg(id);

-- name: ListPromotableReleases :many
-- What `dock promote` carries from an environment: each live service's
-- active release, with the service's name to match in the target.
SELECT services.name AS service_name, sqlc.embed(releases)
FROM services JOIN releases ON releases.service_id = services.id AND releases.status = 'active'
WHERE services.environment_id = +sqlc.arg(environment_id) AND services.archived_at IS NULL
ORDER BY services.name;

-- name: CreatePromotedRelease :one
-- Creates a target service's release from a promoted one: the same image
-- by digest, with the target's own config. image_id is set when the
-- image is already on the target's server (same server, no registry).
INSERT INTO releases (service_id, number, source, image_ref, image_id, image_digest, built_on_server_id,
    promoted_from_release_id, config, host_port, created_by, carried)
VALUES (sqlc.arg(service_id), sqlc.arg(number), 'promote', sqlc.arg(image_ref), sqlc.arg(image_id), sqlc.arg(image_digest),
    sqlc.narg(built_on_server_id), CAST(sqlc.arg(promoted_from_release_id) AS INTEGER), sqlc.arg(config), sqlc.arg(host_port),
    sqlc.arg(created_by), sqlc.arg(carried))
RETURNING *;

-- name: ListKeptDigests :many
-- The registry digests releases still need, which registry pruning keeps
-- (ADR 0014 section 4): every service's newest keep releases (the
-- rollback window) and every active and in-flight release, of services
-- that aren't archived (an archived service is never rolled back or
-- restored, ADR 0020 amendment). It reads each release with a digest
-- once, through the (service_id, number) key for the window and the
-- services primary key; pruning runs at most hourly, after a deploy or an
-- archive.
SELECT DISTINCT releases.image_digest FROM releases
JOIN services ON services.id = releases.service_id AND services.archived_at IS NULL
WHERE releases.image_digest != ''
  AND (releases.status IN ('active', 'queued', 'building', 'starting', 'health_checking')
       OR releases.number > (SELECT max(newest.number) FROM releases newest WHERE newest.service_id = releases.service_id)
                            - CAST(sqlc.arg(keep) AS INTEGER));

-- name: GetInstallID :one
-- This control plane's install ID (migration 0015), the value of the
-- dock.control_plane label on everything it creates.
SELECT install_id FROM install WHERE id = 1;

-- name: ListRegistryServices :many
-- Every service that pushed an image to the registry, for the console's
-- registry page: how many distinct images it keeps there (as
-- ListKeptDigests counts them: none once archived), its newest release
-- with an image, and its active release. It scans services (it lists them
-- all; an admin opening the page runs it) and reads each one's releases
-- through the (service_id, number) key, its active release through
-- releases_one_active.
SELECT
    projects.name AS project,
    environments.name AS environment,
    services.name AS service,
    projects.id AS project_id,
    environments.id AS environment_id,
    services.id AS service_id,
    services.archived_at,
    CAST(count(DISTINCT CASE
        WHEN services.archived_at IS NULL
         AND (releases.status IN ('active', 'queued', 'building', 'starting', 'health_checking')
              OR releases.number > (SELECT max(newest.number) FROM releases newest WHERE newest.service_id = releases.service_id)
                                   - CAST(sqlc.arg(keep) AS INTEGER))
        THEN releases.image_digest END) AS INTEGER) AS kept,
    CAST(max(releases.number) AS INTEGER) AS newest,
    CAST(coalesce((SELECT active.number FROM releases active
                   WHERE active.service_id = services.id AND active.status = 'active'), 0) AS INTEGER) AS active_number
FROM releases
JOIN services ON services.id = releases.service_id
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE releases.image_digest != ''
GROUP BY services.id
ORDER BY projects.name, environments.name, services.name;
