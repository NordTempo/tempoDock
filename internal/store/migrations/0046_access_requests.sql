-- Access requests (ADR 0044): a token's change that a locked environment
-- refused, kept so an admin can approve it, which runs it again as the
-- token. via and operation say how to run it again (the API's operation
-- ID or MCP's tool name); args are its input, sealed under nonce since
-- they can hold variable values. fingerprint makes identical pending
-- requests one. audit_mark is the environment's latest audit event when
-- it was asked, to show what changed since. token_id is the token that
-- asked; a GitHub push has none. upload names a held source archive.
CREATE TABLE access_requests (
    id             INTEGER PRIMARY KEY,
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
    finished_at    TIMESTAMP
);
CREATE INDEX access_requests_org ON access_requests (org_id, id);
CREATE INDEX access_requests_token ON access_requests (token_id, id);
CREATE UNIQUE INDEX access_requests_pending ON access_requests (fingerprint) WHERE status = 'pending';
CREATE INDEX access_requests_open ON access_requests (expires_at) WHERE status IN ('pending', 'approved');
CREATE INDEX access_requests_created ON access_requests (created_at);
-- What changed in an environment since a request was asked.
CREATE INDEX audit_events_environment ON audit_events (environment_id, id);

ALTER TABLE orgs ADD COLUMN request_ttl_hours INTEGER NOT NULL DEFAULT 24;
