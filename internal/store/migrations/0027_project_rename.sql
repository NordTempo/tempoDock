-- Project renaming keeps audit targets as historical text, but indexes
-- their stable environment/service suffix. Alerts use stable service IDs;
-- legacy rows are attributed only when the names identify exactly one service.
DROP INDEX audit_events_target;
CREATE INDEX audit_events_target ON audit_events
    (environment_id, substr(target, instr(target, '/') + 1), id);

DROP INDEX audit_events_data_query;
CREATE INDEX audit_events_data_query ON audit_events
    (environment_id, substr(target, instr(target, '/') + 1), user_id, token_id, id)
    WHERE action = 'data.query';

ALTER TABLE alerts ADD COLUMN project_id INTEGER REFERENCES projects (id);
ALTER TABLE alerts ADD COLUMN service_id INTEGER REFERENCES services (id);
UPDATE alerts SET (project_id, service_id) = (
    SELECT p.id, s.id FROM services s
    JOIN environments e ON e.id = s.environment_id
    JOIN projects p ON p.id = e.project_id
    WHERE p.name = alerts.project AND e.name = alerts.environment AND s.name = alerts.service
) WHERE 1 = (
    SELECT count(*) FROM services s
    JOIN environments e ON e.id = s.environment_id
    JOIN projects p ON p.id = e.project_id
    WHERE p.name = alerts.project AND e.name = alerts.environment AND s.name = alerts.service
);
UPDATE alerts SET key = substr(key, 1, instr(key, ':')) || 'service:' || service_id
WHERE service_id IS NOT NULL AND kind IN ('deploy_failed', 'backup_failed', 'crash_loop');
CREATE INDEX alerts_project_open ON alerts (project_id) WHERE resolved_at IS NULL;
