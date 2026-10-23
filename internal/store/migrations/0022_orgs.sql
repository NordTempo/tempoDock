-- foreign_keys: off
-- Organizations and the platform operator (ADR 0027). Rebuilds projects
-- (names become unique per org) and data_policy_rules (the default scope
-- becomes per org), so it runs with foreign keys off and is checked with
-- foreign_key_check before it commits.

-- slug follows the project name rules and addresses the org (the API's
-- `org` parameter, `dock --org`).
CREATE TABLE orgs (
    id         INTEGER PRIMARY KEY,
    slug       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- A user's role in an org: read, deploy or admin, as users.role was.
-- user_id leads the second index: authentication looks up the caller's
-- memberships on every request.
CREATE TABLE org_members (
    org_id     INTEGER NOT NULL REFERENCES orgs (id),
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('read', 'deploy', 'admin')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, user_id)
) WITHOUT ROWID;
CREATE INDEX org_members_user ON org_members (user_id, org_id);

-- An install with anything in it gets one org holding all of it. Fresh
-- installs create their first org at bootstrap.
INSERT INTO orgs (id, slug, name)
SELECT 1, 'default', 'Default'
WHERE EXISTS (SELECT 1 FROM users) OR EXISTS (SELECT 1 FROM projects) OR EXISTS (SELECT 1 FROM tokens);

-- Operators run the platform (servers, backups, accounts); roles move to
-- memberships. Admins were both.
ALTER TABLE users ADD COLUMN operator BOOLEAN NOT NULL DEFAULT 0;
UPDATE users SET operator = (role = 'admin');
INSERT INTO org_members (org_id, user_id, role) SELECT 1, id, role FROM users;
ALTER TABLE users DROP COLUMN role;

CREATE TABLE projects_new (
    id          INTEGER PRIMARY KEY,
    org_id      INTEGER NOT NULL REFERENCES orgs (id),
    name        TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at DATETIME,
    UNIQUE (org_id, name)
);
INSERT INTO projects_new (id, org_id, name, created_at, archived_at)
SELECT id, 1, name, created_at, archived_at FROM projects;
DROP TABLE projects;
ALTER TABLE projects_new RENAME TO projects;

-- Manual tokens belong to one org; `dock login` tokens (org_id NULL) act
-- for their user in each of the user's orgs. Operator tokens may do what
-- operators do: unlimited admin tokens could manage servers before, so
-- they keep that.
ALTER TABLE tokens ADD COLUMN org_id INTEGER REFERENCES orgs (id);
ALTER TABLE tokens ADD COLUMN operator BOOLEAN NOT NULL DEFAULT 0;
UPDATE tokens SET org_id = 1 WHERE origin = 'manual';
UPDATE tokens SET operator = 1
WHERE origin = 'manual' AND scope = 'admin' AND project_id IS NULL AND environment_id IS NULL;
CREATE INDEX tokens_org ON tokens (org_id) WHERE org_id IS NOT NULL;

-- The org an event happened in; NULL for platform events (servers,
-- accounts, backup settings).
ALTER TABLE audit_events ADD COLUMN org_id INTEGER;
UPDATE audit_events SET org_id = 1 WHERE EXISTS (SELECT 1 FROM orgs);
CREATE INDEX audit_events_org ON audit_events (org_id, id);

-- Policy rules per org: a rule without a project is the org's default.
CREATE TABLE data_policy_rules_new (
    id             INTEGER PRIMARY KEY,
    org_id         INTEGER NOT NULL REFERENCES orgs (id),
    project_id     INTEGER REFERENCES projects (id),
    environment_id INTEGER REFERENCES environments (id),
    subject        TEXT NOT NULL CHECK (subject IN ('role:read', 'role:deploy', 'tokens')),
    kind           TEXT NOT NULL CHECK (kind IN ('production', 'non_production')),
    level          TEXT NOT NULL CHECK (level IN ('none', 'schema', 'read', 'propose', 'write')),
    sensitive      TEXT NOT NULL CHECK (sensitive IN ('masked', 'visible')),
    CHECK (environment_id IS NULL OR project_id IS NOT NULL)
);
INSERT INTO data_policy_rules_new (id, org_id, project_id, environment_id, subject, kind, level, sensitive)
SELECT id, 1, project_id, environment_id, subject, kind, level, sensitive FROM data_policy_rules;
DROP TABLE data_policy_rules;
ALTER TABLE data_policy_rules_new RENAME TO data_policy_rules;
CREATE UNIQUE INDEX data_policy_rules_scope
    ON data_policy_rules (org_id, coalesce(project_id, 0), coalesce(environment_id, 0), subject, kind);

-- The org-wide audit page filtered by action reads its page's IDs from
-- this covering index, as audit_events_action did before orgs.
DROP INDEX audit_events_action;
CREATE INDEX audit_events_org_action ON audit_events (org_id, action, id);
