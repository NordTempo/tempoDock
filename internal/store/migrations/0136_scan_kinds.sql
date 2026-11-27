-- Which environment kinds' images a builder scans for vulnerabilities,
-- apart from which it builds (ADR 0074 amendment 2026-11-27): the
-- control plane can build production while a homelab keeps the scanner's
-- database of gigabytes and scans everything. Until now a builder scanned
-- the kinds it built.
ALTER TABLE servers ADD COLUMN scans_production BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN scans_non_production BOOLEAN NOT NULL DEFAULT 1;
UPDATE servers SET scans_production = builds_production, scans_non_production = builds_non_production;
