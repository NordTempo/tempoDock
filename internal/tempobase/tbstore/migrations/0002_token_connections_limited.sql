-- Whether a token is limited to its token_connections rows. Without the
-- flag, removing a limited token's last connection cascaded its limit
-- away and the token reached every connection; now it reaches none.
-- Tokens whose limit rows are already gone can't be told apart and stay
-- unlimited.
ALTER TABLE tokens ADD COLUMN connections_limited INTEGER NOT NULL DEFAULT 0;
UPDATE tokens SET connections_limited = 1 WHERE id IN (SELECT token_id FROM token_connections);
