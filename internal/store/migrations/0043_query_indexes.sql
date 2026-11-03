-- Indexes for queries that scanned (docs/performance.md, SQLite):
-- removing a user's sessions and counting the live ones of each member
-- (ListOrgMembers), dropping expired sessions at every sign-in, and a
-- site dashboard's deploy markers, read by finish time per service
-- (ListSiteDeploys).
CREATE INDEX sessions_user ON sessions (user_id, expires_at);
CREATE INDEX sessions_expires ON sessions (expires_at);
CREATE INDEX releases_finished ON releases (service_id, finished_at) WHERE status IN ('active', 'superseded');
