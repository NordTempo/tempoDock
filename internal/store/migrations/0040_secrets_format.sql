-- The format of the sealed values (ADR 0041): 1 while values sealed
-- before they were bound to where they are stored may remain, 2 once the
-- daemon has resealed them all. From then on it refuses unbound values.
ALTER TABLE install ADD COLUMN secrets_format INTEGER NOT NULL DEFAULT 1;
