-- Public dashboards' addresses (ADR 0037). stats-<site>.<base domain>
-- was the site's name made a hostname label when read, so sites in any
-- orgs could share one, and which of them it showed was undefined. Each
-- live site now holds its own label, unique across every org. The label
-- is made in Go, so Dock gives the sites from before this migration
-- theirs when it starts, oldest first (core.assignPublicSlugs).
ALTER TABLE sites ADD COLUMN public_slug TEXT;
CREATE UNIQUE INDEX sites_live_public_slug ON sites (public_slug) WHERE archived_at IS NULL;

-- A public domain of the site's own is one live site's too: of sites
-- that share one, the oldest keeps it and the others lose it.
UPDATE sites SET settings = json_remove(settings, '$.public_domain')
WHERE archived_at IS NULL AND json_extract(settings, '$.public_domain') <> ''
  AND EXISTS (
    SELECT 1 FROM sites AS older
    WHERE older.archived_at IS NULL AND older.id < sites.id
      AND json_extract(older.settings, '$.public_domain') = json_extract(sites.settings, '$.public_domain'));
CREATE UNIQUE INDEX sites_live_public_domain ON sites (json_extract(settings, '$.public_domain'))
WHERE archived_at IS NULL AND json_extract(settings, '$.public_domain') <> '';
