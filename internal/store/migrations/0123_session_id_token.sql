-- A single sign-on session keeps the ID token it signed in with, sealed
-- (ADR 0065, RP-initiated logout amendment): signing out of Dock sends it
-- to the provider's end-session endpoint as id_token_hint, so the
-- provider's session ends too. NULL for other sessions.
ALTER TABLE sessions ADD COLUMN sso_id_token BLOB;
