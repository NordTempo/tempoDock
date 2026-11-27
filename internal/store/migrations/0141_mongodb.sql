-- MongoDB as a database template (ADR 0110): the CHECKs on
-- services.template and backups.kind take 'mongodb', and so do the Data
-- panel's (services.database, data_access.engine), which its engine uses
-- next. SQLite can't change a CHECK in place, so each column is swapped
-- for one with the wider CHECK, values kept, as 0087 and 0090 did.
ALTER TABLE services ADD COLUMN template_name TEXT NOT NULL DEFAULT ''
    CHECK (template_name IN ('', 'postgres', 'redis', 'mysql', 'mariadb', 'valkey', 'pgvector', 'mongodb'));
UPDATE services SET template_name = template;
ALTER TABLE services DROP COLUMN template;
ALTER TABLE services RENAME COLUMN template_name TO template;

ALTER TABLE services ADD COLUMN database_setting TEXT NOT NULL DEFAULT ''
    CHECK (database_setting IN ('', 'postgres', 'mysql', 'redis', 'mongodb', 'none'));
UPDATE services SET database_setting = database;
ALTER TABLE services DROP COLUMN database;
ALTER TABLE services RENAME COLUMN database_setting TO database;

ALTER TABLE backups ADD COLUMN kind_name TEXT NOT NULL DEFAULT 'volume'
    CHECK (kind_name IN ('postgres', 'redis', 'mysql', 'mongodb', 'volume', 'pitr'));
UPDATE backups SET kind_name = kind;
ALTER TABLE backups DROP COLUMN kind;
ALTER TABLE backups RENAME COLUMN kind_name TO kind;

ALTER TABLE data_access ADD COLUMN engine_name TEXT NOT NULL DEFAULT 'postgres'
    CHECK (engine_name IN ('postgres', 'mysql', 'redis', 'mongodb'));
UPDATE data_access SET engine_name = engine;
ALTER TABLE data_access DROP COLUMN engine;
ALTER TABLE data_access RENAME COLUMN engine_name TO engine;
