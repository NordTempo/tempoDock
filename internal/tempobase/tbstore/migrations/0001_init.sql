-- tempoBase's state (ADR 0024 section 2): users and how they sign in,
-- saved connections, the data access policy, the data panel's records
-- per connection, and the audit log. Levels are none < schema < read <
-- propose < write; sensitive is masked or visible.

-- People. password_hash is bcrypt; '' until an invite link sets it.
CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    name          TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('viewer', 'editor', 'admin')),
    password_hash TEXT NOT NULL,
    disabled_at   TIMESTAMP,
    created_at    TIMESTAMP NOT NULL
);

-- UI sessions: sha256 of the tbs_ cookie secret, 30 days, not sliding.
CREATE TABLE sessions (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    secret_hash TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMP NOT NULL,
    expires_at  TIMESTAMP NOT NULL
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE INDEX sessions_expires ON sessions (expires_at);

-- Invites (email and role, 7 days) and password resets (a user, 24
-- hours), each used once. secret_hash is the sha256 of the link's code.
CREATE TABLE links (
    id          INTEGER PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('invite', 'reset')),
    email       TEXT NOT NULL COLLATE NOCASE,
    role        TEXT CHECK (role IN ('viewer', 'editor', 'admin')),
    user_id     INTEGER REFERENCES users (id) ON DELETE CASCADE,
    secret_hash TEXT NOT NULL UNIQUE,
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMP NOT NULL,
    expires_at  TIMESTAMP NOT NULL,
    used_at     TIMESTAMP,
    CHECK ((kind = 'invite') = (role IS NOT NULL)),
    CHECK ((kind = 'reset') = (user_id IS NOT NULL))
);
CREATE INDEX links_user ON links (user_id) WHERE user_id IS NOT NULL;
-- Open invites, for the people page.
CREATE INDEX links_open ON links (kind, id) WHERE used_at IS NULL;

-- Saved databases. url_secret is the full URL sealed with the key file;
-- the parts kept in the clear are for listing and never include the
-- password.
CREATE TABLE connections (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    engine     TEXT NOT NULL DEFAULT 'postgres' CHECK (engine IN ('postgres')),
    host       TEXT NOT NULL,
    port       INTEGER NOT NULL,
    database   TEXT NOT NULL,
    username   TEXT NOT NULL,
    tls_mode   TEXT NOT NULL CHECK (tls_mode IN ('disable', 'prefer', 'require', 'verify-ca', 'verify-full')),
    ca_pem     TEXT NOT NULL DEFAULT '',
    url_secret BLOB NOT NULL,
    production INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL,
    created_by TEXT NOT NULL
);

-- API tokens (agents): sha256 of the tbt_ secret. data_* is the token's
-- own data access, all three or none (NULL follows the tokens rule).
CREATE TABLE tokens (
    id                  INTEGER PRIMARY KEY,
    name                TEXT NOT NULL,
    prefix              TEXT NOT NULL,
    secret_hash         TEXT NOT NULL UNIQUE,
    user_id             INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    data_production     TEXT CHECK (data_production IN ('none', 'schema', 'read', 'propose', 'write')),
    data_non_production TEXT CHECK (data_non_production IN ('none', 'schema', 'read', 'propose', 'write')),
    data_sensitive      TEXT CHECK (data_sensitive IN ('masked', 'visible')),
    created_at          TIMESTAMP NOT NULL,
    expires_at          TIMESTAMP,
    revoked_at          TIMESTAMP
);
CREATE INDEX tokens_user ON tokens (user_id);

-- A token's connection limit; none: every connection the policy allows.
CREATE TABLE token_connections (
    token_id      INTEGER NOT NULL REFERENCES tokens (id) ON DELETE CASCADE,
    connection_id INTEGER NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    PRIMARY KEY (token_id, connection_id)
) WITHOUT ROWID;
CREATE INDEX token_connections_connection ON token_connections (connection_id);

-- The data access policy: connection_id NULL is the default. At most one
-- rule per scope, subject and kind.
CREATE TABLE data_policy_rules (
    id            INTEGER PRIMARY KEY,
    connection_id INTEGER REFERENCES connections (id) ON DELETE CASCADE,
    subject       TEXT NOT NULL CHECK (subject IN ('role:read', 'role:deploy', 'tokens')),
    kind          TEXT NOT NULL CHECK (kind IN ('production', 'non_production')),
    level         TEXT NOT NULL CHECK (level IN ('none', 'schema', 'read', 'propose', 'write')),
    sensitive     TEXT NOT NULL CHECK (sensitive IN ('masked', 'visible'))
);
CREATE UNIQUE INDEX data_policy_rules_scope ON data_policy_rules (coalesce(connection_id, 0), subject, kind);
CREATE INDEX data_policy_rules_connection ON data_policy_rules (connection_id);

-- A connection's tempoBase roles, once set up: their passwords sealed
-- with the key file, and what their grants were last synced for.
CREATE TABLE data_access (
    connection_id      INTEGER PRIMARY KEY REFERENCES connections (id) ON DELETE CASCADE,
    engine             TEXT NOT NULL CHECK (engine IN ('postgres')),
    server_version     TEXT NOT NULL,
    reader_secret      BLOB NOT NULL,
    writer_secret      BLOB NOT NULL,
    grants_synced_hash TEXT NOT NULL DEFAULT '',
    set_up_at          TIMESTAMP NOT NULL,
    set_up_by          TEXT NOT NULL
);

-- Columns an admin marked sensitive, per connection.
CREATE TABLE sensitive_columns (
    connection_id INTEGER NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    schema_name   TEXT NOT NULL,
    table_name    TEXT NOT NULL,
    column_name   TEXT NOT NULL,
    PRIMARY KEY (connection_id, schema_name, table_name, column_name)
) WITHOUT ROWID;

-- Named SQL, per user and connection.
CREATE TABLE saved_queries (
    id            INTEGER PRIMARY KEY,
    connection_id INTEGER NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    sql           TEXT NOT NULL,
    created_at    TIMESTAMP NOT NULL,
    updated_at    TIMESTAMP NOT NULL,
    UNIQUE (connection_id, user_id, name)
);
CREATE INDEX saved_queries_user ON saved_queries (user_id);

-- Row changes applied in one transaction (ADR 0016 section 5). changes is
-- the JSON of []apitypes.DataChange, sql the rendered transaction;
-- rows_affected ([]int64) and conflicts ([]apitypes.DataConflict) are JSON
-- set by the decision, '' until then.
CREATE TABLE changesets (
    id            INTEGER PRIMARY KEY,
    connection_id INTEGER NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    author        TEXT NOT NULL,
    author_kind   TEXT NOT NULL CHECK (author_kind IN ('user', 'token')),
    user_id       INTEGER REFERENCES users (id) ON DELETE SET NULL,
    token_id      INTEGER REFERENCES tokens (id) ON DELETE SET NULL,
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'committed', 'rejected', 'expired', 'conflict')),
    message       TEXT NOT NULL DEFAULT '',
    changes       TEXT NOT NULL,
    sql           TEXT NOT NULL,
    rows_affected TEXT NOT NULL DEFAULT '',
    conflicts     TEXT NOT NULL DEFAULT '',
    reason        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL,
    expires_at    TIMESTAMP NOT NULL,
    decided_at    TIMESTAMP,
    decided_by    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX changesets_connection ON changesets (connection_id, id);
-- Pending ones: a connection's list, and the expiry sweep and the
-- per-connection counts.
CREATE INDEX changesets_connection_pending ON changesets (connection_id, id) WHERE status = 'pending';
CREATE INDEX changesets_pending ON changesets (expires_at) WHERE status = 'pending';
CREATE INDEX changesets_user ON changesets (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX changesets_token ON changesets (token_id) WHERE token_id IS NOT NULL;

-- The long part of a data.query or data.commit event (SQL cut at
-- apitypes.AuditSQLMaxBytes), kept out of audit_events.
CREATE TABLE audit_details (
    id          INTEGER PRIMARY KEY,
    sql         TEXT NOT NULL,
    rows        INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    outcome     TEXT NOT NULL CHECK (outcome IN ('ok', 'error', 'timeout', 'canceled', 'conflict'))
);

-- The audit log. No foreign keys to users, tokens or connections: events
-- outlive what they name, and target keeps the name. Deleting a detail
-- (the retention pass) deletes its event.
CREATE TABLE audit_events (
    id            INTEGER PRIMARY KEY,
    created_at    TIMESTAMP NOT NULL,
    actor         TEXT NOT NULL,
    actor_kind    TEXT NOT NULL CHECK (actor_kind IN ('user', 'token', 'system')),
    user_id       INTEGER,
    token_id      INTEGER,
    action        TEXT NOT NULL,
    connection_id INTEGER,
    target        TEXT NOT NULL,
    summary       TEXT NOT NULL,
    detail_id     INTEGER REFERENCES audit_details (id) ON DELETE CASCADE
);
CREATE INDEX audit_events_connection ON audit_events (connection_id, id);
CREATE INDEX audit_events_action ON audit_events (action, id);
CREATE INDEX audit_events_connection_action ON audit_events (connection_id, action, id);
CREATE INDEX audit_events_user_history ON audit_events (action, connection_id, user_id, id);
CREATE INDEX audit_events_token_history ON audit_events (action, connection_id, token_id, id);
CREATE INDEX audit_events_created ON audit_events (created_at);
CREATE INDEX audit_events_detail ON audit_events (detail_id);
