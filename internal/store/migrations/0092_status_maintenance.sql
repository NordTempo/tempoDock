-- Maintenance windows on public status pages (ADR 0073, phase 3). A
-- window is a status_incidents row of kind 'maintenance' (0091 made room
-- for it): starts_at and ends_at are its window, its components carry
-- impact 'maintenance'. While one is in progress, alerts about its
-- services and checks are recorded with muted_by and sent to no channel
-- until it ends; those still open then are sent.
ALTER TABLE alerts ADD COLUMN muted_by INTEGER REFERENCES status_incidents (id) ON DELETE SET NULL;
CREATE INDEX alerts_muted ON alerts (muted_by) WHERE muted_by IS NOT NULL;

-- The windows not finished yet, which Dock reads when it starts and
-- whenever one changes.
CREATE INDEX status_incidents_maintenance ON status_incidents (starts_at)
    WHERE kind = 'maintenance' AND resolved_at IS NULL;
