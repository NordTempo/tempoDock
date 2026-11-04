-- Locked environments (ADR 0044). locked_at is set while an environment is
-- locked: tokens can't change anything in it. open_until opens a locked
-- environment for a while; past it the lock holds again, computed when
-- checked. orgs.lock_production locks new production environments as
-- they are created.
ALTER TABLE environments ADD COLUMN locked_at TIMESTAMP;
ALTER TABLE environments ADD COLUMN locked_by TEXT NOT NULL DEFAULT '';
ALTER TABLE environments ADD COLUMN lock_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE environments ADD COLUMN open_until TIMESTAMP;

ALTER TABLE orgs ADD COLUMN lock_production INTEGER NOT NULL DEFAULT 0;
