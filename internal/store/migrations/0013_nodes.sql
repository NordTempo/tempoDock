-- Remote servers (nodes), join tokens and promotion (ADR 0014; placement
-- columns are ADR 0015's, from 0007).
--
-- Whether a server is online is runtime state held by the hub; the row
-- keeps what outlives a connection. servers.status (0001) is no longer
-- read and stays only because existing queries still write it.

-- The node's pinned key: "sha256:" plus the hex SHA-256 of its public key.
-- NULL for the local server and after a revoke. next_key_fingerprint is a
-- rotation in progress: the node reconnects with it and it is promoted.
-- SQLite can't add UNIQUE columns, so uniqueness is in the indexes (NULLs
-- don't collide), which also serve the per-handshake lookup
-- (GetServerByFingerprint).
ALTER TABLE servers ADD COLUMN key_fingerprint TEXT;
ALTER TABLE servers ADD COLUMN next_key_fingerprint TEXT;
CREATE UNIQUE INDEX servers_key_fingerprint ON servers (key_fingerprint);
CREATE UNIQUE INDEX servers_next_key_fingerprint ON servers (next_key_fingerprint);
-- Set by a revoke (which clears the keys); cleared when the server re-joins.
ALTER TABLE servers ADD COLUMN revoked_at DATETIME;

-- Facts the node reports in hello, read-only on the control plane:
-- ingress mode, the public IPs DNS should point at (JSON array of strings,
-- direct only), the tunnel's CNAME target (tunnel only) and Docker's
-- version. version and memory_bytes (0007) are hello facts too.
ALTER TABLE servers ADD COLUMN ingress TEXT NOT NULL DEFAULT 'direct'
    CHECK (ingress IN ('direct', 'cloudflare_tunnel'));
ALTER TABLE servers ADD COLUMN public_ips TEXT NOT NULL DEFAULT '[]';
ALTER TABLE servers ADD COLUMN tunnel_hostname TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN docker_version TEXT NOT NULL DEFAULT '';
-- A control plane setting: generated hostnames are
-- <service>-<env>.<base_domain> of the environment's server. '' is none.
ALTER TABLE servers ADD COLUMN base_domain TEXT NOT NULL DEFAULT '';
-- When the current (or last) link came up, and why the last one ended
-- ('' while connected or never). last_seen_at (0001) is set on both.
ALTER TABLE servers ADD COLUMN connected_at DATETIME;
ALTER TABLE servers ADD COLUMN disconnect_reason TEXT NOT NULL DEFAULT '';

-- A server's environments: removing a server checks there are none (and
-- the foreign key check looks for them too), and per-server lookups
-- (ListServerEnvironments, host ports) start here.
CREATE INDEX environments_server ON environments (server_id);

-- One-time join tokens (`dock server join`). hash is the SHA-256 of the
-- secret; the rest is what the new server row gets. A token is used once
-- (used_at) and only before expires_at. used_by_server_id is a plain
-- reference so a removed server doesn't take its join record along.
CREATE TABLE join_tokens (
    id                     INTEGER PRIMARY KEY,
    hash                   TEXT NOT NULL UNIQUE,
    server_name            TEXT NOT NULL,
    accepts_production     BOOLEAN NOT NULL DEFAULT 0,
    accepts_non_production BOOLEAN NOT NULL DEFAULT 0,
    builder                BOOLEAN NOT NULL DEFAULT 0,
    created_by             TEXT NOT NULL,
    created_at             DATETIME NOT NULL,
    expires_at             DATETIME NOT NULL,
    used_at                DATETIME,
    used_by_server_id      INTEGER
);
-- Unused tokens: servers still "joining".
CREATE INDEX join_tokens_pending ON join_tokens (expires_at) WHERE used_at IS NULL;

-- image_digest is the registry manifest digest ("sha256:..."; '' for a
-- local-only or CI image), which pulls on other servers and promotes use.
-- built_on_server_id is the builder that built it: a plain reference, so a
-- builder that built releases can still be removed. A promoted release
-- points at the release it was promoted from (another service's, in the
-- source environment); from_release stays a number within one service.
ALTER TABLE releases ADD COLUMN image_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN built_on_server_id INTEGER;
ALTER TABLE releases ADD COLUMN promoted_from_release_id INTEGER REFERENCES releases (id);
