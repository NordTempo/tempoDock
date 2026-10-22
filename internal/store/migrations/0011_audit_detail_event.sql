-- Finds the audit event of a detail (GetAuditEventByDetail), to check who
-- may read the detail.
CREATE INDEX audit_events_detail ON audit_events (detail_id) WHERE detail_id IS NOT NULL;
