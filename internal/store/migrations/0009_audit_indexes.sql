-- A service's audit log (ListAuditEventsByTarget) read its project's whole
-- history through audit_events_project and filtered by target. This index
-- finds one target's events in an environment directly, newest first.
CREATE INDEX audit_events_target ON audit_events (environment_id, target, id);

-- The audit log filtered by action walked the whole log (or project's
-- history) for a rare action. These indexes find the matching events and
-- cover the lookup of a page's IDs, so only the page itself is read
-- (ListAuditEventsByAction, ListProjectAuditEventsByAction).
CREATE INDEX audit_events_action ON audit_events (action, id);
CREATE INDEX audit_events_project_action ON audit_events (project_id, action, environment_id);
