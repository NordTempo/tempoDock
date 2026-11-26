-- Analytics belong to the service (ADR 0103): a site is one service's,
-- and counts every domain of it that doesn't redirect. site_hostnames,
-- which tied history to a hostname, goes.
ALTER TABLE sites ADD COLUMN service_id INTEGER REFERENCES services (id);

-- Each site takes the service of its environment with the most of its
-- hostnames, the earliest added on a tie.
UPDATE sites SET service_id = (
    SELECT sh.service_id FROM site_hostnames sh
    JOIN services s ON s.id = sh.service_id
    WHERE sh.site_id = sites.id AND s.environment_id = sites.environment_id
    GROUP BY sh.service_id
    ORDER BY count(*) DESC, min(sh.added_at), sh.service_id
    LIMIT 1
);

-- A live site with no live service left has nothing to count.
UPDATE sites SET archived_at = CURRENT_TIMESTAMP
WHERE archived_at IS NULL AND (service_id IS NULL
    OR EXISTS (SELECT 1 FROM services s WHERE s.id = sites.service_id AND s.archived_at IS NOT NULL));

-- A service that led two live sites keeps the older.
UPDATE sites SET archived_at = CURRENT_TIMESTAMP
WHERE archived_at IS NULL AND EXISTS (
    SELECT 1 FROM sites o WHERE o.service_id = sites.service_id AND o.archived_at IS NULL AND o.id < sites.id
);

-- A live service whose hostnames another service's site counted gets a
-- site of its own, counting from now, with that site's settings (but not
-- its public domain, which is one site's). It is named after its first
-- such hostname; the public address comes when analytics starts
-- (assignPublicSlugs).
INSERT INTO sites (environment_id, name, settings, created_by, created_at, service_id)
SELECT s.environment_id,
       CASE WHEN EXISTS (
           SELECT 1 FROM sites o JOIN environments oe ON oe.id = o.environment_id
           WHERE oe.project_id = e.project_id AND o.name = x.hostname AND o.archived_at IS NULL
       ) THEN x.hostname || '-2' ELSE x.hostname END,
       json_remove(x.settings, '$.public_domain'), 'dock', CURRENT_TIMESTAMP, s.id
FROM (
    SELECT sh.service_id, min(sh.hostname) AS hostname, min(site.settings) AS settings
    FROM site_hostnames sh
    JOIN sites site ON site.id = sh.site_id
    WHERE site.archived_at IS NULL AND site.service_id <> sh.service_id
    GROUP BY sh.service_id
) x
JOIN services s ON s.id = x.service_id
JOIN environments e ON e.id = s.environment_id
WHERE s.archived_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM sites o WHERE o.service_id = s.id AND o.archived_at IS NULL);

DROP TABLE site_hostnames;

-- One live site per service; the service's latest archived site is
-- found by the second (turning analytics on again brings it back).
CREATE UNIQUE INDEX sites_live_service ON sites (service_id) WHERE archived_at IS NULL;
CREATE INDEX sites_service ON sites (service_id, id);
