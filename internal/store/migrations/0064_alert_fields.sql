-- An alert's CLI command that shows more and what it points at (a site,
-- release, run...) as JSON (apitypes.AlertTarget), so clients link and
-- copy without parsing the message. Empty for older alerts.
ALTER TABLE alerts ADD COLUMN command TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN target TEXT NOT NULL DEFAULT '';
