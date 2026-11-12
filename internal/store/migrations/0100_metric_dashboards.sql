-- App metrics' pinned charts (ADR 0075, phase 6b): a service's dashboard
-- on its Metrics tab, its charts in order as a JSON array of
-- {"name", "agg", "by"} (apitypes.MetricChartSpec). Without a row the
-- service has Dock's default charts, those of the runtime metrics it
-- sends; the first change saves them with it.
CREATE TABLE metric_dashboards (
    service_id INTEGER PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    charts     TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
