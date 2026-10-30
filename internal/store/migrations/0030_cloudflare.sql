-- Cloudflare (ADR 0036): named API-token connections, the zones each can
-- manage, the tunnels Dock runs for tunnel servers and the domains it
-- routes through them. Tokens are sealed.
--
-- Tunnels and records are a queue as much as state: want is what Dock
-- wants (present or absent), status how far Cloudflare is (pending rows
-- are worked on, retry_at holds back a retry). Rows outlive their server
-- or domain (ON DELETE SET NULL) until what Dock made is deleted. gen
-- counts what others asked of a row: the worker saves its result only
-- if gen is what it started from, so a removal or re-add while it called
-- Cloudflare isn't lost.
CREATE TABLE cloudflare_connections (
    id            INTEGER PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    account_id    TEXT NOT NULL,
    account_name  TEXT NOT NULL DEFAULT '',
    token         BLOB NOT NULL,
    status        TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'invalid')),
    status_reason TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL,
    updated_at    TIMESTAMP NOT NULL,
    updated_by    TEXT NOT NULL
);

-- A zone name belongs to one connection: the last one saved that reaches it.
CREATE TABLE cloudflare_zones (
    id            INTEGER PRIMARY KEY,
    connection_id INTEGER NOT NULL REFERENCES cloudflare_connections (id) ON DELETE CASCADE,
    zone_id       TEXT NOT NULL,
    name          TEXT NOT NULL UNIQUE
);
CREATE INDEX cloudflare_zones_connection ON cloudflare_zones (connection_id);

-- server_name stays for messages once the server is removed. adopted:
-- the tunnel was made outside Dock, so Dock never deletes it.
CREATE TABLE cloudflare_tunnels (
    id            INTEGER PRIMARY KEY,
    server_id     INTEGER UNIQUE REFERENCES servers (id) ON DELETE SET NULL,
    server_name   TEXT NOT NULL,
    connection_id INTEGER NOT NULL REFERENCES cloudflare_connections (id),
    tunnel_id     TEXT NOT NULL DEFAULT '',
    token         BLOB,
    adopted       BOOLEAN NOT NULL DEFAULT 0,
    want          TEXT NOT NULL DEFAULT 'present' CHECK (want IN ('present', 'absent')),
    status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ok', 'error')),
    reason        TEXT NOT NULL DEFAULT '',
    attempts      INTEGER NOT NULL DEFAULT 0,
    retry_at      TIMESTAMP,
    gen           INTEGER NOT NULL DEFAULT 0,
    updated_at    TIMESTAMP NOT NULL
);
CREATE INDEX cloudflare_tunnels_due ON cloudflare_tunnels (status, retry_at);

-- The domains Dock routes through a tunnel server's tunnel: added "from
-- Cloudflare", or generated under a base domain in a managed zone. One
-- row per hostname. record_id is the proxied CNAME Dock made ('' for
-- none: not yet, or one that already pointed at the tunnel, status
-- external), content the tunnel hostname it points at, route_tunnel the
-- tunnel Dock added a hostname route to ('' when its catch-all goes to
-- Caddy already).
CREATE TABLE cloudflare_records (
    id            INTEGER PRIMARY KEY,
    hostname      TEXT NOT NULL UNIQUE,
    domain_id     INTEGER REFERENCES domains (id) ON DELETE SET NULL,
    server_id     INTEGER REFERENCES servers (id) ON DELETE SET NULL,
    connection_id INTEGER NOT NULL REFERENCES cloudflare_connections (id) ON DELETE CASCADE,
    zone_id       TEXT NOT NULL,
    record_id     TEXT NOT NULL DEFAULT '',
    content       TEXT NOT NULL DEFAULT '',
    route_tunnel  TEXT NOT NULL DEFAULT '',
    want          TEXT NOT NULL DEFAULT 'present' CHECK (want IN ('present', 'absent')),
    status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ok', 'external', 'error')),
    reason        TEXT NOT NULL DEFAULT '',
    attempts      INTEGER NOT NULL DEFAULT 0,
    retry_at      TIMESTAMP,
    gen           INTEGER NOT NULL DEFAULT 0,
    updated_at    TIMESTAMP NOT NULL
);
CREATE INDEX cloudflare_records_due ON cloudflare_records (status, retry_at);
CREATE INDEX cloudflare_records_domain ON cloudflare_records (domain_id);
CREATE INDEX cloudflare_records_server ON cloudflare_records (server_id);

-- `dock server add --tunnel`: the connection to make the server's tunnel
-- with once it joins.
ALTER TABLE join_tokens ADD COLUMN cloudflare_id INTEGER REFERENCES cloudflare_connections (id) ON DELETE SET NULL;
