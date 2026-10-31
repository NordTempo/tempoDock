-- Traffic statistics count requests only: visitors are Dock Analytics'
-- (ADR 0037), so the estimated visitor counts and the sketches they were
-- merged from go (ADR 0033, amended).
DROP TABLE traffic_visitors;
ALTER TABLE traffic_stats DROP COLUMN visitors;
