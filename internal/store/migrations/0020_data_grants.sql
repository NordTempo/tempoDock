-- Dock's roles in a database no longer get pg_read_all_data and
-- pg_write_all_data, which also read the system catalogs (password hashes,
-- user mapping options, large objects), but explicit grants on the user
-- schemas, synced like dock_reader_masked's column grants (ADR 0016).
-- grants_synced_hash identifies the sensitive columns and schema all of
-- them were last synced for ('' never). Every recorded sync is forgotten,
-- so the next use of the panel syncs the new grants as the owner, which
-- also revokes the predefined roles.
ALTER TABLE data_access RENAME COLUMN masked_synced_hash TO grants_synced_hash;
UPDATE data_access SET grants_synced_hash = '';
