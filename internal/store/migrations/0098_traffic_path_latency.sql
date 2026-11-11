-- Per-route numbers (ADR 0075): each path's 5xx answers and how long its
-- requests took, counted at the edge from every request (never sampled).
-- lat0 to lat9 count requests under 5, 10, 25, 50, 100, 250, 500, 1000
-- and 2500 ms, and past that (node.LatencyBounds). Rows from before have
-- none: their paths show no percentiles.
ALTER TABLE traffic_paths ADD COLUMN errors INTEGER NOT NULL DEFAULT 0;
ALTER TABLE traffic_paths ADD COLUMN lat0 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE traffic_paths ADD COLUMN lat1 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE traffic_paths ADD COLUMN lat2 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE traffic_paths ADD COLUMN lat3 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE traffic_paths ADD COLUMN lat4 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE traffic_paths ADD COLUMN lat5 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE traffic_paths ADD COLUMN lat6 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE traffic_paths ADD COLUMN lat7 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE traffic_paths ADD COLUMN lat8 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE traffic_paths ADD COLUMN lat9 INTEGER NOT NULL DEFAULT 0;
