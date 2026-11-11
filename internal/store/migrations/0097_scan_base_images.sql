-- Image vulnerability scanning, phase 3 (ADR 0074): base image hints and
-- importing a vulnerability database on air-gapped installs.

-- The image a Dockerfile build's final stage was built on (node:22-alpine)
-- and the digest it resolved to then; empty for others.
ALTER TABLE releases ADD COLUMN base_image TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN base_digest TEXT NOT NULL DEFAULT '';

-- The same on an image's scan, taken from its first release. base_moved:
-- the base tag pointed elsewhere at the last daily check (base_checked_at),
-- so a rebuild picks up a newer base image.
ALTER TABLE image_scans ADD COLUMN base_image TEXT NOT NULL DEFAULT '';
ALTER TABLE image_scans ADD COLUMN base_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE image_scans ADD COLUMN base_moved INTEGER NOT NULL DEFAULT 0;
ALTER TABLE image_scans ADD COLUMN base_checked_at INTEGER;
CREATE INDEX image_scans_base ON image_scans (base_image) WHERE base_image != '';

-- A database archive an operator imported (`dock scanning import`): its
-- sha256 here, the file in the data dir; each builder records the one it
-- has imported.
ALTER TABLE platform_settings ADD COLUMN scan_db_import TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN scan_db_import TEXT NOT NULL DEFAULT '';
