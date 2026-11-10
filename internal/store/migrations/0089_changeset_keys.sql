-- A changeset of a key-value database holds key changes instead of row
-- changes (ADR 0072): JSON of []apitypes.DataKeyChange, '' for row
-- changesets.
ALTER TABLE changesets ADD COLUMN key_changes TEXT NOT NULL DEFAULT '';
