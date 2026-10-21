-- Database panel (ADR 0016). Levels are none < schema < read < propose <
-- write; sensitive is masked or visible. Services, projects and environments
-- are archived, never deleted, so rows here keep plain references; a
-- deleted user takes their saved queries along.

-- A token's own data access, overriding the policy's tokens rule. Core sets
-- all three or none; all NULL follows the policy.
ALTER TABLE tokens ADD COLUMN data_production TEXT
    CHECK (data_production IN ('none', 'schema', 'read', 'propose', 'write'));
ALTER TABLE tokens ADD COLUMN data_non_production TEXT
    CHECK (data_non_production IN ('none', 'schema', 'read', 'propose', 'write'));
ALTER TABLE tokens ADD COLUMN data_sensitive TEXT
    CHECK (data_sensitive IN ('masked', 'visible'));

-- Dock-side settings that apply immediately and never create a release.
-- database: '' detects from the image, 'postgres' or 'none' override it.
-- database_url_variable names the variable with the connection URL
-- (DATABASE_URL, or api.DATABASE_URL for another service's); '' uses the
-- service's own DATABASE_URL, then POSTGRES_*.
ALTER TABLE services ADD COLUMN database TEXT NOT NULL DEFAULT ''
    CHECK (database IN ('', 'postgres', 'none'));
ALTER TABLE services ADD COLUMN database_url_variable TEXT NOT NULL DEFAULT '';

-- The data access policy. Scope: both NULL is the default, project_id
-- alone a project, both set an environment (of that project). At most one
-- rule per scope, subject and kind; data_policy_rules_scope also serves
-- every lookup (by scope, and the default/project/environment chain).
CREATE TABLE data_policy_rules (
    id             INTEGER PRIMARY KEY,
    project_id     INTEGER REFERENCES projects (id),
    environment_id INTEGER REFERENCES environments (id),
    subject        TEXT NOT NULL CHECK (subject IN ('role:read', 'role:deploy', 'tokens')),
    kind           TEXT NOT NULL CHECK (kind IN ('production', 'non_production')),
    level          TEXT NOT NULL CHECK (level IN ('none', 'schema', 'read', 'propose', 'write')),
    sensitive      TEXT NOT NULL CHECK (sensitive IN ('masked', 'visible')),
    CHECK (environment_id IS NULL OR project_id IS NOT NULL)
);
CREATE UNIQUE INDEX data_policy_rules_scope
    ON data_policy_rules (coalesce(project_id, 0), coalesce(environment_id, 0), subject, kind);

-- A database's Dock roles, once set up. The secrets are the dock_reader and
-- dock_writer passwords sealed with internal/secrets; they never leave the
-- daemon. masked_synced_hash identifies the sensitive columns and schema
-- the dock_reader_masked grants were last synced for ('' never).
CREATE TABLE data_access (
    service_id         INTEGER PRIMARY KEY REFERENCES services (id),
    engine             TEXT NOT NULL CHECK (engine IN ('postgres')),
    server_version     TEXT NOT NULL DEFAULT '',
    reader_secret      BLOB NOT NULL,
    writer_secret      BLOB NOT NULL,
    masked_synced_hash TEXT NOT NULL DEFAULT '',
    set_up_at          DATETIME NOT NULL,
    set_up_by          TEXT NOT NULL
);

-- Columns an admin marked sensitive, per database.
CREATE TABLE sensitive_columns (
    service_id  INTEGER NOT NULL REFERENCES services (id),
    schema_name TEXT NOT NULL,
    table_name  TEXT NOT NULL,
    column_name TEXT NOT NULL,
    PRIMARY KEY (service_id, schema_name, table_name, column_name)
) WITHOUT ROWID;

-- Named SQL, per user and database.
CREATE TABLE saved_queries (
    id         INTEGER PRIMARY KEY,
    service_id INTEGER NOT NULL REFERENCES services (id),
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    sql        TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (service_id, user_id, name)
);
CREATE INDEX saved_queries_user ON saved_queries (user_id);

-- Row changes applied in one transaction: pending until committed,
-- rejected, expired (expires_at passed) or failed with a conflict. author,
-- author_kind, user_id and token_id identify the actor like audit_events
-- (no foreign keys: the record outlives users). changes is the JSON of
-- []apitypes.DataChange, sql the rendered transaction; rows_affected
-- ([]int64) and conflicts ([]apitypes.DataConflict) are JSON set by the
-- decision, '' until then.
CREATE TABLE changesets (
    id            INTEGER PRIMARY KEY,
    service_id    INTEGER NOT NULL REFERENCES services (id),
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'committed', 'rejected', 'expired', 'conflict')),
    author        TEXT NOT NULL,
    author_kind   TEXT NOT NULL CHECK (author_kind IN ('user', 'token')),
    user_id       INTEGER,
    token_id      INTEGER,
    message       TEXT NOT NULL DEFAULT '',
    changes       TEXT NOT NULL,
    sql           TEXT NOT NULL,
    rows_affected TEXT NOT NULL DEFAULT '',
    conflicts     TEXT NOT NULL DEFAULT '',
    reason        TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL,
    expires_at    DATETIME NOT NULL,
    decided_at    DATETIME,
    decided_by    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX changesets_service ON changesets (service_id, id);
-- Pending ones: the Changes list and the expiry sweep.
CREATE INDEX changesets_pending ON changesets (service_id, id) WHERE status = 'pending';
CREATE INDEX changesets_pending_expiry ON changesets (expires_at) WHERE status = 'pending';

-- The long part of an audit event (SQL, cut at apitypes.AuditSQLMaxBytes by
-- the caller), kept out of audit_events so the log stays scannable.
CREATE TABLE audit_details (
    id          INTEGER PRIMARY KEY,
    sql         TEXT NOT NULL,
    rows        INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    outcome     TEXT NOT NULL CHECK (outcome IN ('ok', 'error', 'timeout', 'canceled', 'conflict'))
);
ALTER TABLE audit_events ADD COLUMN detail_id INTEGER REFERENCES audit_details (id);
-- Query history: one actor's data.query events on one database.
CREATE INDEX audit_events_data_query ON audit_events (target, user_id, token_id, id)
    WHERE action = 'data.query';
