-- How many runs of a scheduled job may run at once (ADR 0054, amended
-- 2026-11-27). Until now overlap skip meant one and allow meant the
-- service's 4 concurrent runs; those are the values existing jobs get.
ALTER TABLE service_jobs ADD COLUMN concurrency INTEGER NOT NULL DEFAULT 1 CHECK (concurrency BETWEEN 1 AND 16);
UPDATE service_jobs SET concurrency = 4 WHERE overlap = 'allow';
