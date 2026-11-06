-- What a failed snapshot's job printed (its last 16 KB), for "View log"
-- on the service's Backups tab. Empty for a snapshot that succeeded, so
-- the lists stay small: a service keeps at most a handful of snapshots.
ALTER TABLE volume_snapshots ADD COLUMN log TEXT NOT NULL DEFAULT '';
