-- Dock Analytics' sites (ADR 0037).

-- name: CreateSite :one
INSERT INTO sites (environment_id, name, created_by, created_at) VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetSite :one
SELECT * FROM sites WHERE id = +?;

-- GetSiteByName finds a live site by name in a project; names are
-- unique per project (core.createSite).
-- name: GetSiteByName :one
SELECT sites.* FROM sites
JOIN environments ON environments.id = sites.environment_id
WHERE environments.project_id = +sqlc.arg(project_id) AND sites.name = +sqlc.arg(name) AND sites.archived_at IS NULL
ORDER BY sites.id LIMIT 1;

-- ListProjectSites returns a project's sites, live first, with their
-- environment's name.
-- name: ListProjectSites :many
SELECT sites.*, environments.name AS environment_name FROM sites
JOIN environments ON environments.id = sites.environment_id
WHERE environments.project_id = +?
ORDER BY sites.archived_at IS NOT NULL, sites.name, sites.id;

-- name: SetSiteSettings :exec
UPDATE sites SET settings = ? WHERE id = +?;

-- name: RenameSite :exec
UPDATE sites SET name = ? WHERE id = +?;

-- name: SetSiteEnvironment :exec
UPDATE sites SET environment_id = ? WHERE id = +?;

-- name: ArchiveSite :exec
UPDATE sites SET archived_at = ? WHERE id = +? AND archived_at IS NULL;

-- name: GetSiteHostname :one
SELECT * FROM site_hostnames WHERE hostname = +?;

-- ListSiteHostnames returns the hostnames of the given sites with the
-- name of the service each last served ('' for none).
-- name: ListSiteHostnames :many
SELECT site_hostnames.*, CAST(coalesce(services.name, '') AS TEXT) AS service_name
FROM site_hostnames
LEFT JOIN services ON services.id = site_hostnames.service_id
WHERE site_hostnames.site_id IN (sqlc.slice(site_ids))
ORDER BY site_hostnames.hostname;

-- name: AddSiteHostname :exec
INSERT INTO site_hostnames (hostname, site_id, service_id, added_at) VALUES (?, ?, ?, ?)
ON CONFLICT (hostname) DO UPDATE SET site_id = excluded.site_id, service_id = excluded.service_id;

-- name: SetSiteHostnameService :exec
UPDATE site_hostnames SET service_id = ? WHERE hostname = +?;

-- name: RemoveSiteHostname :exec
DELETE FROM site_hostnames WHERE hostname = +?;

-- name: CountSiteHostnames :one
SELECT count(*) FROM site_hostnames WHERE site_id = +?;

-- SiteOfService returns a live site that one of the service's hostnames
-- belongs to, if any: enabling another hostname of the service joins it.
-- name: SiteOfService :one
SELECT sites.* FROM site_hostnames
JOIN sites ON sites.id = site_hostnames.site_id
WHERE site_hostnames.service_id = +? AND sites.archived_at IS NULL
ORDER BY sites.id LIMIT 1;

-- name: AddSiteMarker :exec
INSERT INTO site_markers (site_id, at, kind, message) VALUES (?, ?, ?, ?);

-- ListSiteMarkers returns a site's markers in [from, to), oldest first.
-- name: ListSiteMarkers :many
SELECT * FROM site_markers WHERE site_id = +sqlc.arg(site_id) AND at >= +sqlc.arg(from_at) AND at < +sqlc.arg(to_at) ORDER BY at;

-- ListSiteDeploys returns the releases that went live in [from, to) for
-- the services serving a site's hostnames, oldest first: deploy markers.
-- name: ListSiteDeploys :many
SELECT releases.number, releases.finished_at, services.name AS service_name
FROM releases
JOIN services ON services.id = releases.service_id
WHERE releases.service_id IN (SELECT DISTINCT site_hostnames.service_id FROM site_hostnames WHERE site_hostnames.site_id = +sqlc.arg(site_id))
  AND releases.status IN ('active', 'superseded')
  AND releases.finished_at >= +sqlc.arg(from_at) AND releases.finished_at < +sqlc.arg(to_at)
ORDER BY releases.finished_at
LIMIT 200;

-- ListAnalyticsHosts returns, for routing, every live site's hostnames
-- on a server's environments: the hostname and its site.
-- name: ListAnalyticsHosts :many
SELECT site_hostnames.hostname, site_hostnames.site_id
FROM site_hostnames
JOIN sites ON sites.id = site_hostnames.site_id
JOIN environments ON environments.id = sites.environment_id
WHERE environments.server_id = +? AND sites.archived_at IS NULL;

-- ListLiveSites returns every live site with its settings, for the
-- ingest's per-site rules.
-- name: ListLiveSites :many
SELECT id, settings FROM sites WHERE archived_at IS NULL;

-- name: SetAnalyticsSiteDefaults :exec
UPDATE analytics_settings SET site_defaults = ?, updated_by = ?, updated_at = ? WHERE id = 1;

-- DomainScope returns a domain with its service, environment and
-- project, for operations named by hostname.
-- name: DomainScope :one
SELECT domains.hostname, domains.redirect_to, services.id AS service_id, services.name AS service_name,
       environments.id AS environment_id, environments.name AS environment_name, environments.server_id,
       projects.id AS project_id, projects.name AS project_name, projects.org_id
FROM domains
JOIN services ON services.id = domains.service_id
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE domains.hostname = +?;

-- ServiceNames names services by ID (analytics breakdowns by service).
-- name: ServiceNames :many
SELECT id, name FROM services WHERE id IN (sqlc.slice(ids));

-- ReleaseNames names releases for the dashboard's Releases breakdown:
-- their service's name and their number.
-- name: ReleaseNames :many
SELECT r.id, s.name, r.number FROM releases r JOIN services s ON s.id = r.service_id
WHERE r.id IN (sqlc.slice(ids));

-- ListAllSites returns every site's settings, archived ones too: the
-- daily retention pass.
-- name: ListAllSites :many
SELECT id, settings FROM sites ORDER BY id;
