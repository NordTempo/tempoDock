-- Dock Analytics' sites (ADR 0037): the sets of hostnames analytics
-- belong to, in one environment of a project, so staging and production
-- never mix. A hostname belongs to at most one site. It stays with its
-- site when its domain is removed, so its history carries on when the
-- domain comes back on another service; turning analytics off for it
-- removes it.
CREATE TABLE sites (
    id             INTEGER PRIMARY KEY,
    environment_id INTEGER NOT NULL REFERENCES environments (id),
    name           TEXT NOT NULL,
    -- The site's own settings, as JSON; a missing key follows the
    -- install's defaults (analytics_settings.site_defaults).
    settings       TEXT NOT NULL DEFAULT '{}',
    archived_at    TIMESTAMP,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMP NOT NULL
);
CREATE INDEX sites_environment ON sites (environment_id);
CREATE UNIQUE INDEX sites_live_name ON sites (environment_id, name) WHERE archived_at IS NULL;

-- service_id is the service the hostname last served: a domain coming
-- back on another one is a move, marked on the site's charts.
CREATE TABLE site_hostnames (
    hostname   TEXT PRIMARY KEY,
    site_id    INTEGER NOT NULL REFERENCES sites (id),
    service_id INTEGER REFERENCES services (id),
    added_at   TIMESTAMP NOT NULL
);
CREATE INDEX site_hostnames_site ON site_hostnames (site_id);
CREATE INDEX site_hostnames_service ON site_hostnames (service_id);

-- What happened to a site, as markers on its charts: hostnames added,
-- moved to another service, or removed. Deploys come from releases.
CREATE TABLE site_markers (
    id      INTEGER PRIMARY KEY,
    site_id INTEGER NOT NULL REFERENCES sites (id),
    at      TIMESTAMP NOT NULL,
    kind    TEXT NOT NULL CHECK (kind IN ('added', 'moved', 'removed')),
    message TEXT NOT NULL
);
CREATE INDEX site_markers_site ON site_markers (site_id, at);

-- The install's defaults for every site's settings, as JSON.
ALTER TABLE analytics_settings ADD COLUMN site_defaults TEXT NOT NULL DEFAULT '{}';
