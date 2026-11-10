-- The database setting takes every engine of the data panel (ADR 0072):
-- 0010 allowed only '', 'postgres' and 'none', so setting a service to
-- mysql (or redis) failed. SQLite can't change a CHECK in place, so the
-- column is swapped for one with the wider CHECK, values kept.
ALTER TABLE services ADD COLUMN database_setting TEXT NOT NULL DEFAULT ''
    CHECK (database_setting IN ('', 'postgres', 'mysql', 'redis', 'none'));
UPDATE services SET database_setting = database;
ALTER TABLE services DROP COLUMN database;
ALTER TABLE services RENAME COLUMN database_setting TO database;
