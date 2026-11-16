-- The app template (ADR 0083) a service was created from, and the
-- template's revision then: shown on the service, never read to run it.
ALTER TABLE services ADD COLUMN from_template TEXT NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN from_template_revision INTEGER NOT NULL DEFAULT 0;
