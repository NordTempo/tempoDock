-- A person's agents (ADR 0080, phase 6): their `dock login` tokens act
-- for them with what this lets them use of their access, per area role
-- and environment kind, and ask them first where ask is set. A row
-- without token_id is the person's setting for all their agents in the
-- org; one with token_id narrows that one agent further. No row: the
-- agent may use everything the person has but approving.
CREATE TABLE agent_settings (
    user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    org_id   INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    token_id INTEGER REFERENCES tokens (id) ON DELETE CASCADE,
    area     TEXT NOT NULL,
    kind     INTEGER NOT NULL CHECK (kind IN (1, 2, 4)),
    -- level is one of the area role's levels, 'all' for its highest, or
    -- 'off'.
    level    TEXT NOT NULL,
    ask      BOOLEAN NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX agent_settings_key ON agent_settings (user_id, org_id, coalesce(token_id, 0), area, kind);

-- A change an agent's owner asked to approve first goes to them, not to
-- the environment's approvers.
ALTER TABLE access_requests ADD COLUMN ask_user_id INTEGER REFERENCES users (id) ON DELETE CASCADE;
CREATE INDEX access_requests_ask ON access_requests (ask_user_id, id) WHERE ask_user_id IS NOT NULL;
