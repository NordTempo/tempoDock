-- GitHub push-to-deploy (ADR 0018).
--
-- One connection per service: a repository, a branch and an optional
-- subdirectory. hook_id is the random, unguessable part of the webhook URL
-- (/hooks/github/<hook_id>). secret (the webhook's HMAC key) and token (a
-- fine-grained GitHub token, NULL for public repositories) are sealed with
-- the secrets box: secret must be read back to verify signatures, so it
-- can't be hashed.
--
-- pending_* is the newest push waiting to deploy (empty: none). A newer
-- push replaces it, which is how the newest push wins; the worker clears
-- it once its release exists. pending_force skips the "already deployed"
-- check (manual `dock git deploy`). last_event says what happened to the
-- latest delivery, for `dock git status`.
CREATE TABLE git_connections (
    id              INTEGER PRIMARY KEY,
    service_id      INTEGER NOT NULL UNIQUE REFERENCES services (id),
    hook_id         TEXT NOT NULL UNIQUE,
    repo            TEXT NOT NULL,
    branch          TEXT NOT NULL,
    dir             TEXT NOT NULL DEFAULT '',
    secret          BLOB NOT NULL,
    token           BLOB,
    post_status     BOOLEAN NOT NULL DEFAULT 0,
    pending_sha     TEXT NOT NULL DEFAULT '',
    pending_message TEXT NOT NULL DEFAULT '',
    pending_author  TEXT NOT NULL DEFAULT '',
    pending_by      TEXT NOT NULL DEFAULT '',
    pending_force   BOOLEAN NOT NULL DEFAULT 0,
    last_event      TEXT NOT NULL DEFAULT '',
    last_event_at   DATETIME,
    created_by      TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- Connections with a push waiting (pending_sha > ''), read at start-up.
CREATE INDEX git_connections_pending ON git_connections (pending_sha);

-- Webhook delivery IDs seen recently (X-GitHub-Delivery), so a replayed
-- or redelivered request is answered without deploying again. Rows older
-- than the replay window are deleted as new ones arrive.
CREATE TABLE git_deliveries (
    delivery_id TEXT PRIMARY KEY,
    created_at  DATETIME NOT NULL
) WITHOUT ROWID;
CREATE INDEX git_deliveries_created_at ON git_deliveries (created_at);

-- The commit a release runs. Set on releases built from a push (or `dock
-- git deploy`), and copied by releases that reuse their image (config,
-- rollback, restart, promote), so "what code is live" stays answerable.
ALTER TABLE releases ADD COLUMN commit_sha TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN commit_message TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN commit_author TEXT NOT NULL DEFAULT '';
