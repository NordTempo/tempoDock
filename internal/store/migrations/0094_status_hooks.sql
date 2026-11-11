-- Update hooks of public status pages (ADR 0073, phase 5): where Dock
-- posts each public update of a page's incidents and maintenance, set by
-- the page's admins. config is sealed (sealedStatusHook): the URL, which
-- carries Slack's and Discord's token, and a webhook's signing secret.
-- target is the URL's scheme and host, what lists show.
CREATE TABLE status_hooks (
    id            INTEGER PRIMARY KEY,
    page_id       INTEGER NOT NULL REFERENCES status_pages (id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('slack', 'discord', 'webhook')),
    target        TEXT NOT NULL,
    config        BLOB NOT NULL,
    created_at    DATETIME NOT NULL,
    created_by    TEXT NOT NULL,
    last_sent_at  TIMESTAMP,
    last_error    TEXT NOT NULL DEFAULT '',
    last_error_at TIMESTAMP
);
CREATE UNIQUE INDEX status_hooks_name ON status_hooks (page_id, name);
