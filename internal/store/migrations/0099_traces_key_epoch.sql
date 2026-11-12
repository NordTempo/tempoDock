-- Revoking trace ingest tokens (ADR 0075): the trace key is derived from
-- the analytics token and this epoch, so rotating it (dock tracing
-- rotate-key) bumps the epoch and every token minted before stops
-- working. 0 is the key from before rotation existed.
ALTER TABLE analytics_settings ADD COLUMN traces_key_epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE analytics_settings ADD COLUMN traces_key_rotated_at TIMESTAMP;
