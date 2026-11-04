-- Stopping a service on purpose (ADR 0050). While stopped_at is set the
-- service is stopped: its active release stays active but has no
-- container, and its domains answer 503. The next release of the service
-- that goes live clears it. stopped_by names who stopped it.
ALTER TABLE services ADD COLUMN stopped_at DATETIME;
ALTER TABLE services ADD COLUMN stopped_by TEXT NOT NULL DEFAULT '';
