-- Restore targets (ADR 0095, part 3): a service a restore made is found
-- by the restore row that names it, to refuse it as a target of
-- `dock service switch-references` until that restore succeeded. Snapshot
-- restores had the index (volume_snapshot_restores_to); backups get it.
CREATE INDEX backups_restored_to ON backups (restored_to) WHERE restored_to IS NOT NULL;
