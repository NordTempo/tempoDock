-- foreign_keys: off
-- Access requests, groups, grants and runs are deleted (with their
-- project, by revoking, with their job), and SQLite hands a new row the
-- highest ID in use plus one: the next one would get the deleted one's ID.
-- "Access request #9" then named several requests, `dock request wait 9`
-- could follow another project's request, revoking a grant by an ID from
-- an older listing could end someone else's, and audit entries and alerts
-- naming "run 8" pointed at another run. AUTOINCREMENT never hands out an
-- ID twice. As in 0103, the tables are rebuilt as they are, column for
-- column (columns added later stay last), with their indexes; foreign keys
-- are off while they are swapped and checked before the migration commits.

CREATE TABLE access_requests_new (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    nonce          TEXT NOT NULL UNIQUE,
    org_id         INTEGER NOT NULL REFERENCES orgs (id),
    project_id     INTEGER NOT NULL REFERENCES projects (id),
    environment_id INTEGER NOT NULL REFERENCES environments (id),
    status         TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'done', 'failed')),
    via            TEXT NOT NULL,
    operation      TEXT NOT NULL,
    title          TEXT NOT NULL,
    action         TEXT NOT NULL,
    args           BLOB NOT NULL,
    fingerprint    TEXT NOT NULL,
    reason         TEXT NOT NULL DEFAULT '',
    token_id       INTEGER REFERENCES tokens (id),
    requested_by   TEXT NOT NULL,
    audit_mark     INTEGER NOT NULL,
    upload         TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL,
    expires_at     TIMESTAMP NOT NULL,
    decided_by     TEXT NOT NULL DEFAULT '',
    decided_at     TIMESTAMP,
    comment        TEXT NOT NULL DEFAULT '',
    result         TEXT NOT NULL DEFAULT '',
    error          TEXT NOT NULL DEFAULT '',
    finished_at    TIMESTAMP,
    ask_user_id    INTEGER REFERENCES users (id) ON DELETE CASCADE,
    user_id        INTEGER REFERENCES users (id) ON DELETE CASCADE
);
INSERT INTO access_requests_new SELECT * FROM access_requests;
DROP TABLE access_requests;
ALTER TABLE access_requests_new RENAME TO access_requests;
CREATE INDEX access_requests_org ON access_requests (org_id, id);
CREATE INDEX access_requests_token ON access_requests (token_id, id);
CREATE UNIQUE INDEX access_requests_pending ON access_requests (fingerprint) WHERE status = 'pending';
CREATE INDEX access_requests_open ON access_requests (expires_at) WHERE status IN ('pending', 'approved');
CREATE INDEX access_requests_created ON access_requests (created_at);
CREATE INDEX access_requests_ask ON access_requests (ask_user_id, id) WHERE ask_user_id IS NOT NULL;
CREATE INDEX access_requests_user ON access_requests (user_id, id) WHERE user_id IS NOT NULL;

CREATE TABLE groups_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id     INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    seeded     TEXT CHECK (seeded IN ('admins', 'developers', 'viewers', 'approvers')),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, name)
);
INSERT INTO groups_new SELECT * FROM groups;
DROP TABLE groups;
ALTER TABLE groups_new RENAME TO groups;
CREATE UNIQUE INDEX groups_seeded ON groups (org_id, seeded) WHERE seeded IS NOT NULL;

CREATE TABLE grants_new (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
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
INSERT INTO grants_new SELECT * FROM grants;
DROP TABLE grants;
ALTER TABLE grants_new RENAME TO grants;
CREATE INDEX grants_group ON grants (group_id) WHERE group_id IS NOT NULL;
CREATE INDEX grants_user ON grants (user_id, org_id) WHERE user_id IS NOT NULL;
CREATE INDEX grants_token ON grants (token_id) WHERE token_id IS NOT NULL;
CREATE INDEX grants_project ON grants (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX grants_environment ON grants (environment_id) WHERE environment_id IS NOT NULL;
CREATE INDEX grants_role ON grants (role_id) WHERE role_id IS NOT NULL;

CREATE TABLE service_runs_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id      INTEGER NOT NULL REFERENCES services (id),
    -- The active release whose image, variables and limits it ran with;
    -- NULL for a scheduled run skipped because there was none.
    release_id      INTEGER REFERENCES releases (id),
    trigger         TEXT NOT NULL CHECK (trigger IN ('command', 'schedule', 'manual')),
    command         TEXT NOT NULL,
    tty             BOOLEAN NOT NULL DEFAULT 0,
    volumes         TEXT NOT NULL CHECK (volumes IN ('none', 'read', 'write')),
    timeout_seconds INTEGER NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'timed_out', 'canceled', 'interrupted', 'skipped')),
    -- NULL when the command never exited by itself (it couldn't start,
    -- timed out or was canceled).
    exit_code       INTEGER,
    error           TEXT NOT NULL DEFAULT '',
    -- The tail of its output; empty for terminal sessions.
    output          TEXT NOT NULL DEFAULT '',
    -- Who ran it, as the audit log names actors.
    actor           TEXT NOT NULL,
    actor_kind      TEXT NOT NULL,
    user_id         INTEGER,
    token_id        INTEGER,
    started_at      TIMESTAMP NOT NULL,
    finished_at     TIMESTAMP,
    job_id          INTEGER REFERENCES service_jobs (id),
    snapshot_id     INTEGER,
    cause           TEXT NOT NULL DEFAULT ''
);
INSERT INTO service_runs_new SELECT * FROM service_runs;
DROP TABLE service_runs;
ALTER TABLE service_runs_new RENAME TO service_runs;
CREATE INDEX service_runs_service ON service_runs (service_id, id);
CREATE INDEX service_runs_running ON service_runs (status) WHERE status = 'running';
CREATE INDEX service_runs_job ON service_runs (job_id, id) WHERE job_id IS NOT NULL;
