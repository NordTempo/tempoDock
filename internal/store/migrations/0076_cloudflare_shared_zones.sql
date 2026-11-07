-- A zone may be on several Cloudflare connections: the platform's and
-- any number of orgs' own (ADR 0062, "Zones"). Its name is unique per
-- connection instead of install-wide. Rebuilt to drop the UNIQUE on
-- name; nothing references the table.
CREATE TABLE cloudflare_zones_new (
    id            INTEGER PRIMARY KEY,
    connection_id INTEGER NOT NULL REFERENCES cloudflare_connections (id) ON DELETE CASCADE,
    zone_id       TEXT NOT NULL,
    name          TEXT NOT NULL,
    UNIQUE (connection_id, name)
);
INSERT INTO cloudflare_zones_new (id, connection_id, zone_id, name)
SELECT id, connection_id, zone_id, name FROM cloudflare_zones;
DROP TABLE cloudflare_zones;
ALTER TABLE cloudflare_zones_new RENAME TO cloudflare_zones;
