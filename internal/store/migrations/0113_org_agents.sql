-- foreign_keys: off
-- Org agents (ADR 0080, phase 6b): an organization's tokens hold grants
-- and join groups like people. grants gets a third holder, token_id, so
-- its CHECK changes and SQLite rebuilds it (nothing references it);
-- group_agents are tokens' groups. tokens.granted marks a token whose
-- scope and limit are grants now: the core turns older ones into grants
-- at start, new ones get them when made.
CREATE TABLE grants_new (
    id             INTEGER PRIMARY KEY,
    org_id         INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    group_id       INTEGER REFERENCES groups (id) ON DELETE CASCADE,
    user_id        INTEGER REFERENCES users (id) ON DELETE CASCADE,
    token_id       INTEGER REFERENCES tokens (id) ON DELETE CASCADE,
    role           TEXT NOT NULL,
    role_id        INTEGER REFERENCES roles (id),
    project_id     INTEGER REFERENCES projects (id) ON DELETE CASCADE,
    environment_id INTEGER REFERENCES environments (id) ON DELETE CASCADE,
    kinds          INTEGER NOT NULL DEFAULT 7 CHECK (kinds BETWEEN 1 AND 7),
    expires_at     INTEGER,
    via            TEXT,
    granted_by     TEXT NOT NULL,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((group_id IS NOT NULL) + (user_id IS NOT NULL) + (token_id IS NOT NULL) = 1)
);
INSERT INTO grants_new (id, org_id, group_id, user_id, role, role_id, project_id, environment_id, kinds, expires_at, via, granted_by, created_at)
SELECT id, org_id, group_id, user_id, role, role_id, project_id, environment_id, kinds, expires_at, via, granted_by, created_at FROM grants;
DROP TABLE grants;
ALTER TABLE grants_new RENAME TO grants;
CREATE INDEX grants_group ON grants (group_id) WHERE group_id IS NOT NULL;
CREATE INDEX grants_user ON grants (user_id, org_id) WHERE user_id IS NOT NULL;
CREATE INDEX grants_token ON grants (token_id) WHERE token_id IS NOT NULL;
CREATE INDEX grants_project ON grants (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX grants_environment ON grants (environment_id) WHERE environment_id IS NOT NULL;
CREATE INDEX grants_role ON grants (role_id) WHERE role_id IS NOT NULL;

CREATE TABLE group_agents (
    group_id INTEGER NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    token_id INTEGER NOT NULL REFERENCES tokens (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, token_id)
) WITHOUT ROWID;
CREATE INDEX group_agents_token ON group_agents (token_id, group_id);

ALTER TABLE tokens ADD COLUMN granted BOOLEAN NOT NULL DEFAULT 0;
-- own_grants: an admin gave or ended one of its grants or put it in a
-- group. Until then its grants are its scope's at its limit, which the
-- core works out without reading them on every request.
ALTER TABLE tokens ADD COLUMN own_grants BOOLEAN NOT NULL DEFAULT 0;
