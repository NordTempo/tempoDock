-- Backups on storage connections (ADR 0039): the bucket backups go to is
-- a storage connection (0029) and a folder in it; storage_id NULL keeps
-- them in a local repository on each server. The old repository URL and
-- credentials columns stay for installs not converted yet (the daemon
-- turns them into a connection on start) and are empty after that.
ALTER TABLE backup_settings ADD COLUMN storage_id INTEGER REFERENCES storage_connections (id);
ALTER TABLE backup_settings ADD COLUMN storage_path TEXT NOT NULL DEFAULT '';

-- A move points each backup at its copy, and a converted URL relabels
-- the history, by repository and snapshot.
CREATE INDEX backups_snapshot ON backups (repository, snapshot_id);
