-- An environment that follows a Docker Compose file in a GitHub repository
-- (ADR 0110): a push to its branch that changes the file (or a file it
-- reads) is converted and planned, and its changes staged for review like
-- `dock import compose --stage`. One per environment, read through the
-- organization's GitHub App installation (installation_id, repo_id).
CREATE TABLE compose_sources (
    id              INTEGER PRIMARY KEY,
    environment_id  INTEGER NOT NULL UNIQUE REFERENCES environments (id),
    installation_id INTEGER NOT NULL,
    repo_id         INTEGER NOT NULL,
    repo            TEXT NOT NULL,
    branch          TEXT NOT NULL,
    path            TEXT NOT NULL,
    -- reads are the other files the last sync read (the .env next to the
    -- Compose file, its env_file entries), one path per line from the
    -- repository's root: a push that changes one syncs too.
    reads           TEXT NOT NULL DEFAULT '',
    -- prune stages archives for services the file drops (set by an admin);
    -- auto_apply applies a sync that only adds and changes (ADR 0110).
    prune           BOOLEAN NOT NULL DEFAULT 0,
    auto_apply      BOOLEAN NOT NULL DEFAULT 0,
    -- A push waiting to be synced, latest wins; pending_by is who pushed.
    pending_sha     TEXT NOT NULL DEFAULT '',
    pending_by      TEXT NOT NULL DEFAULT '',
    -- The last sync: its commit, when, and what came of it.
    last_sha        TEXT NOT NULL DEFAULT '',
    last_message    TEXT NOT NULL DEFAULT '',
    last_author     TEXT NOT NULL DEFAULT '',
    last_at         DATETIME,
    outcome         TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'staged', 'unchanged', 'applied', 'failed')),
    summary         TEXT NOT NULL DEFAULT '',
    created_by      TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- App push deliveries are routed by repository and installation.
CREATE INDEX compose_sources_repo ON compose_sources (repo_id, installation_id);
-- Sources with a push waiting (pending_sha > ''), read at start-up.
CREATE INDEX compose_sources_pending ON compose_sources (pending_sha) WHERE pending_sha > '';
