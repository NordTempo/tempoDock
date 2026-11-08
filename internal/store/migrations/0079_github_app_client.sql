-- The GitHub App's OAuth client (ADR 0066): after an install, Dock sends
-- the browser through the App's authorize page and checks with the
-- person's token that they can access the installation before claiming
-- it. client_id is '' and client_secret NULL until known: Apps made before
-- this get them from an operator (`dock github app secret`).
ALTER TABLE github_app ADD COLUMN client_id TEXT NOT NULL DEFAULT '';
ALTER TABLE github_app ADD COLUMN client_secret BLOB;
