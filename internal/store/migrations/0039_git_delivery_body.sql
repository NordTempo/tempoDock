-- Webhook deliveries are also remembered by a hash of their hook and
-- signed body: the X-GitHub-Delivery header isn't signed, so a captured
-- body resent under a new delivery ID must be recognised by its content.
-- Rows from before this migration have none (NULLs never collide) and
-- are pruned with the rest after the replay window.
ALTER TABLE git_deliveries ADD COLUMN body_hash BLOB;
CREATE UNIQUE INDEX git_deliveries_body_hash ON git_deliveries (body_hash);
