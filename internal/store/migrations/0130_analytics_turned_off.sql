-- Analytics an operator turned off (ADR 0101), apart from analytics never
-- turned on: tracing makes the settings row without enabling analytics.
-- While turned off, a site's first domain doesn't turn it on again and
-- the servers' ingests get no salts, so they keep nothing.
ALTER TABLE analytics_settings ADD COLUMN turned_off BOOLEAN NOT NULL DEFAULT 0;
