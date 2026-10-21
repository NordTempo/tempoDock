-- Server pools and placement (ADR 0015).
--
-- Environments get a kind that never changes. Existing ones named
-- "production" are production, everything else non-production.
ALTER TABLE environments ADD COLUMN kind TEXT NOT NULL DEFAULT 'non_production'
    CHECK (kind IN ('production', 'non_production'));
UPDATE environments SET kind = 'production' WHERE name = 'production';

-- A server's single role becomes two switches. A lone server took every
-- environment under the old rules ("with one server everything goes
-- there"), so it keeps accepting both kinds.
ALTER TABLE servers ADD COLUMN accepts_production BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN accepts_non_production BOOLEAN NOT NULL DEFAULT 0;
UPDATE servers SET
    accepts_production = (role = 'production' OR (SELECT count(*) FROM servers) = 1),
    accepts_non_production = (role = 'non-production' OR (SELECT count(*) FROM servers) = 1);
ALTER TABLE servers DROP COLUMN role;
-- No new placements while set; pins still may.
ALTER TABLE servers ADD COLUMN disabled_for_placement BOOLEAN NOT NULL DEFAULT 0;
-- Facts the node reports: total memory (placement weighs free room
-- against it) and its dock version.
ALTER TABLE servers ADD COLUMN memory_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN version TEXT NOT NULL DEFAULT '';

-- Where a project's environments of one kind live. The first environment
-- of a kind picks the server (or an admin pins it), and every later one
-- follows.
CREATE TABLE project_placements (
    project_id INTEGER NOT NULL REFERENCES projects (id),
    kind       TEXT NOT NULL CHECK (kind IN ('production', 'non_production')),
    server_id  INTEGER NOT NULL REFERENCES servers (id),
    -- Set by an admin rather than chosen automatically.
    pinned     BOOLEAN NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, kind)
) WITHOUT ROWID;
CREATE INDEX project_placements_server ON project_placements (server_id);

-- Environments stay where they are. Should a project already have
-- environments of one kind on different servers, they all stay and the
-- most recently created one's server becomes the placement.
INSERT INTO project_placements (project_id, kind, server_id)
SELECT e.project_id, e.kind, e.server_id FROM environments e
WHERE e.id = (
    SELECT max(e2.id) FROM environments e2 WHERE e2.project_id = e.project_id AND e2.kind = e.kind
);

-- Resource statistics. Timestamps are unix seconds; res is the resolution
-- in seconds: 60 (kept 24 hours) or 600 (kept 7 days). Time comes first in
-- the key so inserts append and pruning is a range delete. CPU is in
-- hundredths of a percent; memory and disk in bytes; IO in bytes per second.
CREATE TABLE server_stats (
    res         INTEGER NOT NULL,
    at          INTEGER NOT NULL,
    server_id   INTEGER NOT NULL,
    cpu         INTEGER NOT NULL,
    memory      INTEGER NOT NULL,
    disk        INTEGER NOT NULL,
    load1       INTEGER NOT NULL,
    PRIMARY KEY (res, at, server_id)
) WITHOUT ROWID;

CREATE TABLE service_stats (
    res        INTEGER NOT NULL,
    at         INTEGER NOT NULL,
    service_id INTEGER NOT NULL,
    cpu        INTEGER NOT NULL,
    memory     INTEGER NOT NULL,
    io_read    INTEGER NOT NULL,
    io_write   INTEGER NOT NULL,
    PRIMARY KEY (res, at, service_id)
) WITHOUT ROWID;
