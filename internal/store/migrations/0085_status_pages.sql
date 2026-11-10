-- Public status pages (ADR 0073, phase 1): an organization's pages, their
-- components and what each component's status is built from. A page is
-- served at status-<slug>.<base domain> once published; the slug is one
-- page's across every org, so nobody can take or shadow another's.
-- History comes from uptime_stats, which gains day rows (res 86400, kept
-- 90 days) on the rollup it already has: no table here.
CREATE TABLE status_pages (
    id          INTEGER PRIMARY KEY,
    org_id      INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    slug        TEXT NOT NULL,
    title       TEXT NOT NULL,
    published   BOOLEAN NOT NULL DEFAULT 0,
    -- Day percentages under the bars and per component.
    show_uptime BOOLEAN NOT NULL DEFAULT 1,
    created_at  DATETIME NOT NULL,
    created_by  TEXT NOT NULL
);
CREATE UNIQUE INDEX status_pages_slug ON status_pages (slug);
CREATE INDEX status_pages_org ON status_pages (org_id);

-- A page's components in order. name is public and typed by a person;
-- auto off means only incidents and maintenance (later phases) set its
-- status.
CREATE TABLE status_components (
    id          INTEGER PRIMARY KEY,
    page_id     INTEGER NOT NULL REFERENCES status_pages (id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    grp         TEXT NOT NULL DEFAULT '',
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    auto        BOOLEAN NOT NULL DEFAULT 1
);
CREATE INDEX status_components_page ON status_components (page_id, position);

-- What a component is built from: a service (its uptime checks and its
-- health) or one uptime check.
CREATE TABLE status_sources (
    id           INTEGER PRIMARY KEY,
    component_id INTEGER NOT NULL REFERENCES status_components (id) ON DELETE CASCADE,
    service_id   INTEGER REFERENCES services (id) ON DELETE CASCADE,
    check_id     INTEGER REFERENCES uptime_checks (id) ON DELETE CASCADE,
    CHECK ((service_id IS NULL) != (check_id IS NULL))
);
CREATE INDEX status_sources_component ON status_sources (component_id);
CREATE INDEX status_sources_service ON status_sources (service_id);
CREATE INDEX status_sources_check ON status_sources (check_id);
