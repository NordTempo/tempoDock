-- Operators administer every org (ADR 0062). A suspended org's apps keep
-- running, but its deploys and changes are refused. A deleted org that
-- had projects is kept, hidden, for their history: its projects are
-- archived and its members, tokens and invites removed.
ALTER TABLE orgs ADD COLUMN suspended_at DATETIME;
ALTER TABLE orgs ADD COLUMN suspended_by TEXT NOT NULL DEFAULT '';
ALTER TABLE orgs ADD COLUMN deleted_at DATETIME;

-- "Join as operator": an operator's membership that ends by itself at
-- expires_at (unix seconds). Lookups ignore it from then on; the
-- retention pass removes the row and audits it. NULL for every other
-- membership.
ALTER TABLE org_members ADD COLUMN expires_at INTEGER;
CREATE INDEX org_members_expiring ON org_members (expires_at) WHERE expires_at IS NOT NULL;

-- The console lists every org's own admins, first admin (the owner)
-- first.
CREATE INDEX org_members_admins ON org_members (org_id, created_at) WHERE role = 'admin' AND expires_at IS NULL;
