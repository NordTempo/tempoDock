-- Data access records every engine of the data panel (ADR 0072): 0010
-- allowed only 'postgres', so setting up a MySQL or Redis database failed
-- when its roles were stored, after they were created. 0087 widened the
-- services.database CHECK and missed this one. The column is swapped
-- like 0087's, values kept.
ALTER TABLE data_access ADD COLUMN engine_name TEXT NOT NULL DEFAULT 'postgres'
    CHECK (engine_name IN ('postgres', 'mysql', 'redis'));
UPDATE data_access SET engine_name = engine;
ALTER TABLE data_access DROP COLUMN engine;
ALTER TABLE data_access RENAME COLUMN engine_name TO engine;
