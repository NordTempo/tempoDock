-- Uptime checks Dock adds by itself (ADR 0059): one per verified domain,
-- removed with the domain. A user may remove one like any other check.
ALTER TABLE uptime_checks ADD COLUMN auto BOOLEAN NOT NULL DEFAULT 0;
