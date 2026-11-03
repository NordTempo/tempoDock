-- The format of the sealed values (ADR 0041): 1 while values sealed
-- before they were bound to where they are stored may remain, 2 once
-- tempoBase has resealed them all. From then on it refuses unbound values.
CREATE TABLE install (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    secrets_format INTEGER NOT NULL DEFAULT 1
);
INSERT INTO install (id) VALUES (1);
