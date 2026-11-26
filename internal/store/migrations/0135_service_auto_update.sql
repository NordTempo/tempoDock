-- Automatic image updates (ADR 0074, amendment 2026-11-26): a service
-- deployed by tag follows it on its own. When the daily check sees the
-- tag point at a newer image, Dock snapshots the service's volumes and
-- deploys that image by digest. Off (0) unless someone turns it on.
ALTER TABLE services ADD COLUMN auto_update INTEGER NOT NULL DEFAULT 0;
