-- Analytics belong to the service (ADR 0104): a site is one service's,
-- and counts every verified domain of it that doesn't redirect.
-- site_hostnames, which tied history to a hostname, goes.
ALTER TABLE sites ADD COLUMN service_id INTEGER REFERENCES services (id);

-- Which sites counted before this migration archives any, and which live
-- services they counted: a service keeps counting whatever happens to
-- the site it counted for below.
CREATE TEMP TABLE counted_before AS
SELECT sh.hostname, sh.site_id, sh.service_id, sites.settings
FROM site_hostnames sh
JOIN sites ON sites.id = sh.site_id
JOIN services s ON s.id = sh.service_id
WHERE sites.archived_at IS NULL AND s.archived_at IS NULL;

-- Each site takes the live service of its environment with the most of
-- its hostnames, the earliest added on a tie; an archived site the
-- service it last counted, so turning analytics on again brings it back.
UPDATE sites SET service_id = (
    SELECT sh.service_id FROM site_hostnames sh
    JOIN services s ON s.id = sh.service_id
    WHERE sh.site_id = sites.id AND s.environment_id = sites.environment_id
      AND (s.archived_at IS NULL OR sites.archived_at IS NOT NULL)
    GROUP BY sh.service_id
    ORDER BY count(*) DESC, min(sh.added_at), sh.service_id
    LIMIT 1
);

-- A live site with no live service has nothing to count.
UPDATE sites SET archived_at = CURRENT_TIMESTAMP
WHERE archived_at IS NULL AND service_id IS NULL;

-- A service that led two live sites keeps the older.
UPDATE sites SET archived_at = CURRENT_TIMESTAMP
WHERE archived_at IS NULL AND EXISTS (
    SELECT 1 FROM sites o WHERE o.service_id = sites.service_id AND o.archived_at IS NULL AND o.id < sites.id
);

-- A live service that a site counted but that leads no live site now gets
-- a site of its own, counting from now. It is named after its first such
-- hostname (with the service's ID when the project has a live site of
-- that name, and a random part too if even that is taken, so the
-- upgrade never fails on a name) and takes that hostname's site's
-- settings, but not its
-- public domain, which is one site's. The public address comes when
-- analytics starts (assignPublicSlugs).
INSERT INTO sites (environment_id, name, settings, created_by, created_at, service_id)
SELECT s.environment_id,
       CASE
           WHEN NOT EXISTS (
               SELECT 1 FROM sites o JOIN environments oe ON oe.id = o.environment_id
               WHERE oe.project_id = e.project_id AND o.name = c.hostname AND o.archived_at IS NULL
           ) THEN c.hostname
           WHEN NOT EXISTS (
               SELECT 1 FROM sites o JOIN environments oe ON oe.id = o.environment_id
               WHERE oe.project_id = e.project_id AND o.name = c.hostname || '-' || s.id AND o.archived_at IS NULL
           ) THEN c.hostname || '-' || s.id
           ELSE c.hostname || '-' || s.id || '-' || lower(hex(randomblob(4)))
       END,
       json_remove(c.settings, '$.public_domain'), 'dock', CURRENT_TIMESTAMP, s.id
FROM counted_before c
JOIN services s ON s.id = c.service_id
JOIN environments e ON e.id = s.environment_id
WHERE c.hostname = (SELECT min(c2.hostname) FROM counted_before c2 WHERE c2.service_id = c.service_id)
  AND NOT EXISTS (SELECT 1 FROM sites o WHERE o.service_id = s.id AND o.archived_at IS NULL);

DROP TABLE counted_before;
DROP TABLE site_hostnames;

-- One live site per service; the service's last archived site is found
-- by the second (turning analytics on again brings it back).
CREATE UNIQUE INDEX sites_live_service ON sites (service_id) WHERE archived_at IS NULL;
CREATE INDEX sites_service ON sites (service_id, id);
