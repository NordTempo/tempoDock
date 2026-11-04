-- Deploys into a locked environment wait for approval (ADR 0044). A
-- GitHub push queued there is held as an access request; pending_approved
-- marks a queued push someone may deploy there (an approved request, or a
-- deploy started by a person), which the git worker then deploys.
ALTER TABLE git_connections ADD COLUMN pending_approved INTEGER NOT NULL DEFAULT 0;
