-- Groups and grants (ADR 0080, phase 4): what a person may do in an org
-- comes from the role grants of their groups and their own one-off grants.
-- The core seeds each org's groups (Admins, Developers, Viewers,
-- Approvers) and puts its members in them by their role, at start
-- (seedAccess), so the grants equal today's roles.
--
-- groups: an org's job functions. seeded marks one of the four every
-- org starts with; they can be renamed, changed or removed like others.
CREATE TABLE groups (
    id         INTEGER PRIMARY KEY,
    org_id     INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    seeded     TEXT CHECK (seeded IN ('admins', 'developers', 'viewers', 'approvers')),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, name)
);
CREATE UNIQUE INDEX groups_seeded ON groups (org_id, seeded) WHERE seeded IS NOT NULL;

CREATE TABLE group_members (
    group_id INTEGER NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
) WITHOUT ROWID;
-- Authentication reads a person's groups on every request.
CREATE INDEX group_members_user ON group_members (user_id, group_id);

-- grants: a built-in role (role: "deploy:edit", "approver", "full") at a
-- place (project and environment NULL: the whole org; environment NULL:
-- the whole project), for the environment kinds in kinds (1 production,
-- 2 other, 4 previews). A group's, or one person's for a one-off.
-- expires_at (unix seconds) ends it by itself: a joined operator's, an
-- approved request's.
CREATE TABLE grants (
    id             INTEGER PRIMARY KEY,
    org_id         INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    group_id       INTEGER REFERENCES groups (id) ON DELETE CASCADE,
    user_id        INTEGER REFERENCES users (id) ON DELETE CASCADE,
    role           TEXT NOT NULL,
    project_id     INTEGER REFERENCES projects (id) ON DELETE CASCADE,
    environment_id INTEGER REFERENCES environments (id) ON DELETE CASCADE,
    kinds          INTEGER NOT NULL DEFAULT 7 CHECK (kinds BETWEEN 1 AND 7),
    expires_at     INTEGER,
    via            TEXT,
    granted_by     TEXT NOT NULL,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((group_id IS NULL) != (user_id IS NULL))
);
CREATE INDEX grants_group ON grants (group_id) WHERE group_id IS NOT NULL;
CREATE INDEX grants_user ON grants (user_id, org_id) WHERE user_id IS NOT NULL;
CREATE INDEX grants_project ON grants (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX grants_environment ON grants (environment_id) WHERE environment_id IS NOT NULL;

-- An environment's kind for grants is on its row, so a check needs no
-- query: production or other (environments.kind), and previews.
ALTER TABLE environments ADD COLUMN preview BOOLEAN NOT NULL DEFAULT 0;
UPDATE environments SET preview = 1 WHERE id IN (SELECT environment_id FROM previews);
