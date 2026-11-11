-- Tracing over OTLP: whether the trace store runs beside the analytics
-- store, on the analytics server, with its token. Operators turn it on;
-- it doesn't need analytics on.
ALTER TABLE analytics_settings ADD COLUMN traces_enabled BOOLEAN NOT NULL DEFAULT 0;
