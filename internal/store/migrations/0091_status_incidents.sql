-- Incidents on public status pages (ADR 0073, phase 2): what people say
-- about an outage, update by update, and the impact it has on each
-- component. One table holds incidents and (phase 3) maintenance
-- windows, so its kinds and states are listed in full now.
CREATE TABLE status_incidents (
    id           INTEGER PRIMARY KEY,
    page_id      INTEGER NOT NULL REFERENCES status_pages (id) ON DELETE CASCADE,
    -- Random, in public URLs and the CLI: it counts nothing.
    public_id    TEXT NOT NULL,
    kind         TEXT NOT NULL DEFAULT 'incident' CHECK (kind IN ('incident', 'maintenance')),
    title        TEXT NOT NULL,
    -- The last update's state; draft until the first update is posted.
    state        TEXT NOT NULL CHECK (state IN ('draft', 'investigating', 'identified', 'monitoring', 'resolved',
                                                'scheduled', 'in_progress', 'completed', 'cancelled')),
    -- A draft's words, posted as its first update when it is published.
    draft        TEXT NOT NULL DEFAULT '',
    starts_at    TIMESTAMP NOT NULL,
    ends_at      TIMESTAMP,
    published_at TIMESTAMP,
    resolved_at  TIMESTAMP,
    -- The alert Dock drafted it from; a draft goes when its alert
    -- resolves first.
    alert_id     INTEGER REFERENCES alerts (id) ON DELETE SET NULL,
    created_at   DATETIME NOT NULL,
    created_by   TEXT NOT NULL
);
CREATE UNIQUE INDEX status_incidents_public ON status_incidents (public_id);
CREATE INDEX status_incidents_page ON status_incidents (page_id, starts_at);
CREATE INDEX status_incidents_open ON status_incidents (page_id) WHERE resolved_at IS NULL;
CREATE INDEX status_incidents_alert ON status_incidents (alert_id) WHERE alert_id IS NOT NULL;

-- What an incident does to each component it names. Kept after it is
-- resolved: the bars colour the days it ran.
CREATE TABLE status_incident_components (
    incident_id  INTEGER NOT NULL REFERENCES status_incidents (id) ON DELETE CASCADE,
    component_id INTEGER NOT NULL REFERENCES status_components (id) ON DELETE CASCADE,
    impact       TEXT NOT NULL CHECK (impact IN ('degraded', 'partial_outage', 'major_outage', 'maintenance')),
    PRIMARY KEY (incident_id, component_id)
) WITHOUT ROWID;
CREATE INDEX status_incident_components_component ON status_incident_components (component_id);

-- An incident's updates, public once posted. body is plain text.
CREATE TABLE status_updates (
    id          INTEGER PRIMARY KEY,
    incident_id INTEGER NOT NULL REFERENCES status_incidents (id) ON DELETE CASCADE,
    state       TEXT NOT NULL,
    body        TEXT NOT NULL,
    at          TIMESTAMP NOT NULL,
    created_by  TEXT NOT NULL,
    edited_at   TIMESTAMP
);
CREATE INDEX status_updates_incident ON status_updates (incident_id, at);

-- Whether alerts open draft incidents on the page.
ALTER TABLE status_pages ADD COLUMN drafts BOOLEAN NOT NULL DEFAULT 1;
