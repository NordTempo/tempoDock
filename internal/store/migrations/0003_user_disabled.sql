-- Disabled users can't log in and their sessions are gone; their tokens
-- are separate credentials and keep working until revoked.
ALTER TABLE users ADD COLUMN disabled_at DATETIME;
