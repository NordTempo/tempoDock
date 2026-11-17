-- Previews start with a copy of the base's database (ADR 0092).
--
-- service_previews.data says what a service's volumes start with in a new
-- preview: empty, its newest snapshot (what seed was), a copy of the base's
-- live database, or that copy with the Data panel's sensitive columns
-- masked. data_forks gives pull requests from forks the data too; without
-- it they start empty, also when seeded from a snapshot.
--
-- preview_data is each preview database's copy: from which base service,
-- how, and how it went (copying, copied, or empty with the reason).
ALTER TABLE service_previews ADD COLUMN data TEXT NOT NULL DEFAULT 'empty'
    CHECK (data IN ('empty', 'snapshot', 'copy', 'masked'));
ALTER TABLE service_previews ADD COLUMN data_forks BOOLEAN NOT NULL DEFAULT 0;
UPDATE service_previews SET data = 'snapshot' WHERE seed;
ALTER TABLE service_previews DROP COLUMN seed;

CREATE TABLE preview_data (
    service_id      INTEGER PRIMARY KEY REFERENCES services (id),
    preview_id      INTEGER NOT NULL REFERENCES previews (id),
    from_service_id INTEGER NOT NULL REFERENCES services (id),
    mode            TEXT NOT NULL CHECK (mode IN ('empty', 'snapshot', 'copy', 'masked')),
    status          TEXT NOT NULL CHECK (status IN ('copying', 'copied', 'empty')),
    message         TEXT NOT NULL DEFAULT '',
    bytes           INTEGER NOT NULL DEFAULT 0,
    masked_tables   INTEGER NOT NULL DEFAULT 0,
    masked_columns  INTEGER NOT NULL DEFAULT 0,
    started_at      DATETIME NOT NULL,
    finished_at     DATETIME
);
CREATE INDEX preview_data_preview ON preview_data (preview_id);
-- Copies a restart cut short are ended at the next start.
CREATE INDEX preview_data_copying ON preview_data (status) WHERE status = 'copying';
