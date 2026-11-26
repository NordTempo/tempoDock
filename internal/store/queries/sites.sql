-- Dock Analytics' sites (ADR 0037).

-- name: CreateSite :one
INSERT INTO sites (environment_id, service_id, name, public_slug, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- LiveSiteOfService returns the service's live site: the one its domains
-- count for (ADR 0104).
-- name: LiveSiteOfService :one
SELECT * FROM sites WHERE service_id = +? AND archived_at IS NULL;

-- LastArchivedSiteOfService returns the service's site archived last:
-- the one it counted for before analytics was turned off, which turning
-- it on again brings back.
-- name: LastArchivedSiteOfService :one
SELECT * FROM sites WHERE service_id = +? AND archived_at IS NOT NULL
ORDER BY archived_at DESC, id DESC LIMIT 1;

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

-- name: SetSitePublicSlug :exec
UPDATE sites SET public_slug = ? WHERE id = +?;

-- PublicSlugHolder returns the live site holding a public dashboard
-- address (stats-<slug>.<base domain>), in any org.
-- name: PublicSlugHolder :one
SELECT id FROM sites WHERE public_slug = +sqlc.arg(slug) AND archived_at IS NULL;

-- PublicDomainHolder returns the live site whose public domain is
-- domain, in any org. The terms match sites_live_public_domain's.
-- name: PublicDomainHolder :one
SELECT id FROM sites
WHERE json_extract(settings, '$.public_domain') = +CAST(sqlc.arg(domain) AS TEXT)
  AND json_extract(settings, '$.public_domain') <> '' AND archived_at IS NULL;

-- ListSitesWithoutPublicSlug returns the live sites from before public
-- addresses were stored (migration 0037), oldest first.
-- name: ListSitesWithoutPublicSlug :many
SELECT id, name FROM sites WHERE public_slug IS NULL AND archived_at IS NULL ORDER BY id;

-- ListPublicSites returns every live site's public address and settings,
-- oldest first: the public dashboards' hostnames, the oldest site's
-- where two would share one.
-- name: ListPublicSites :many
SELECT id, public_slug, settings FROM sites WHERE archived_at IS NULL ORDER BY id;

-- SetSiteService hands a live site to another service, under name
-- (dock analytics move), only while it is still where it was moved from.
-- name: SetSiteService :execrows
UPDATE sites SET service_id = ?, environment_id = ?, name = ?
WHERE id = +sqlc.arg(id) AND environment_id = +sqlc.arg(from_environment)
  AND service_id IS +sqlc.arg(from_service) AND archived_at IS NULL;

-- name: ArchiveSite :exec
UPDATE sites SET archived_at = ? WHERE id = +? AND archived_at IS NULL;

-- ArchiveServiceSite archives a service's live site, with the service.
-- name: ArchiveServiceSite :execrows
UPDATE sites SET archived_at = ? WHERE service_id = +? AND archived_at IS NULL;

-- name: UnarchiveSite :exec
UPDATE sites SET archived_at = NULL, name = ?, settings = ? WHERE id = +?;

-- ListSiteHostnames returns the hostnames the given sites count: their
-- service's verified domains that don't redirect, with the service's
-- name. An unverified one might be another org's.
-- name: ListSiteHostnames :many
SELECT sites.id AS site_id, domains.hostname, services.name AS service_name, services.environment_id
FROM sites
JOIN services ON services.id = sites.service_id
JOIN domains ON domains.service_id = sites.service_id AND domains.redirect_to = '' AND domains.verified_at IS NOT NULL
WHERE sites.id IN (sqlc.slice(site_ids))
ORDER BY domains.generated, domains.hostname;

-- name: AddSiteMarker :exec
INSERT INTO site_markers (site_id, at, kind, message) VALUES (?, ?, ?, ?);

-- ListSiteMarkers returns a site's markers in [from, to), oldest first.
-- name: ListSiteMarkers :many
SELECT * FROM site_markers WHERE site_id = +sqlc.arg(site_id) AND at >= +sqlc.arg(from_at) AND at < +sqlc.arg(to_at) ORDER BY at;

-- ListSiteDeploys returns the releases of a site's service that went
-- live in [from, to), oldest first: deploy markers.
-- name: ListSiteDeploys :many
SELECT releases.number, releases.finished_at, services.name AS service_name
FROM releases
JOIN services ON services.id = releases.service_id
WHERE releases.service_id = (SELECT sites.service_id FROM sites WHERE sites.id = +sqlc.arg(site_id))
  AND releases.status IN ('active', 'superseded')
  AND releases.finished_at >= +sqlc.arg(from_at) AND releases.finished_at < +sqlc.arg(to_at)
ORDER BY releases.finished_at
LIMIT 200;

-- ListAnalyticsServices returns, for routing, the counted services on a
-- server's environments with their live site.
-- name: ListAnalyticsServices :many
SELECT sites.service_id, sites.id AS site_id
FROM sites
JOIN environments ON environments.id = sites.environment_id
WHERE environments.server_id = +? AND sites.archived_at IS NULL AND sites.service_id IS NOT NULL;

-- ListLiveSites returns every live site with its settings, for the
-- ingest's per-site rules.
-- name: ListLiveSites :many
SELECT id, name, settings FROM sites WHERE archived_at IS NULL;

-- name: SetAnalyticsSiteDefaults :exec
UPDATE analytics_settings SET site_defaults = ?, updated_by = ?, updated_at = ? WHERE id = 1;

-- DomainScope returns an org's domain with its service, environment and
-- project, for operations named by hostname. Orgs may each claim a
-- hostname until one verifies it (ADR 0042); within an org it is one
-- service's.
-- name: DomainScope :one
SELECT domains.hostname, domains.redirect_to, services.id AS service_id, services.name AS service_name,
       environments.id AS environment_id, environments.name AS environment_name, environments.server_id,
       projects.id AS project_id, projects.name AS project_name, projects.org_id
FROM domains
JOIN services ON services.id = domains.service_id
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE domains.hostname = +sqlc.arg(hostname) AND projects.org_id = +sqlc.arg(org_id);

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

-- ListSiteGoals returns a site's goals in the order they were added.
-- name: ListSiteGoals :many
SELECT * FROM site_goals WHERE site_id = +? ORDER BY id;

-- name: AddSiteGoal :one
INSERT INTO site_goals (site_id, name, kind, match, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: RemoveSiteGoal :execrows
DELETE FROM site_goals WHERE site_id = +sqlc.arg(site_id) AND name = +sqlc.arg(name);

-- ListSiteFunnels returns a site's funnels in the order they were added.
-- name: ListSiteFunnels :many
SELECT * FROM site_funnels WHERE site_id = +? ORDER BY id;

-- name: GetSiteFunnel :one
SELECT * FROM site_funnels WHERE site_id = +sqlc.arg(site_id) AND name = +sqlc.arg(name);

-- name: AddSiteFunnel :one
INSERT INTO site_funnels (site_id, name, steps, created_by, created_at) VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: RemoveSiteFunnel :execrows
DELETE FROM site_funnels WHERE site_id = +sqlc.arg(site_id) AND name = +sqlc.arg(name);

-- name: SiteServing :one
-- Whether a site's service is live, not stopped (ADR 0050), in an
-- environment that isn't asleep, and has a verified domain that doesn't
-- redirect: a site without one has no traffic to expect.
SELECT CAST(EXISTS (
    SELECT 1 FROM sites
    JOIN services ON services.id = sites.service_id
    JOIN environments ON environments.id = services.environment_id
    WHERE sites.id = +? AND services.archived_at IS NULL AND services.stopped_at IS NULL
      AND environments.asleep_at IS NULL
      AND EXISTS (SELECT 1 FROM domains WHERE domains.service_id = services.id AND domains.redirect_to = '' AND domains.verified_at IS NOT NULL)
) AS BOOLEAN) AS serving;

-- ListOrgSites returns an organization's live sites in live projects, by
-- project and name, with their project and environment's name: the
-- Analytics area's list.
-- name: ListOrgSites :many
SELECT sqlc.embed(sites), sqlc.embed(projects), environments.name AS environment_name FROM sites
JOIN environments ON environments.id = sites.environment_id
JOIN projects ON projects.id = environments.project_id
WHERE projects.org_id = +? AND projects.archived_at IS NULL AND sites.archived_at IS NULL
ORDER BY projects.name, sites.name, sites.id;
