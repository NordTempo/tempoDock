-- A hello fact: the node's Caddy serves apps over http:// only ([caddy]
-- disable_tls in direct mode), so domains on it need no certificate and
-- their URLs are http://. The local server's comes from its own config.
ALTER TABLE servers ADD COLUMN plain_http BOOLEAN NOT NULL DEFAULT 0;
