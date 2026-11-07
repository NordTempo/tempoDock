-- Preview database seeds (ADR 0056): seed_from_snapshot is a JSON list of
-- the base's service names ("db") whose volumes each new preview fills
-- from that service's latest ready snapshot (ADR 0047), a copy into the
-- preview's own volume, before the service's first deploy.
ALTER TABLE preview_settings ADD COLUMN seed_from_snapshot TEXT NOT NULL DEFAULT '[]';
