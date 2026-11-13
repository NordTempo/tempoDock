-- Dock's own email (ADR 0079): the platform's SMTP server, set by
-- operators, and the outbox every mail goes through.
--
-- email_settings has one row while email is set up. password is sealed,
-- bound to its column (a one-row table, ADR 0041). The last_* columns
-- are the outcome of the latest attempt to reach the server.
CREATE TABLE email_settings (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    host          TEXT NOT NULL,
    port          INTEGER NOT NULL,
    security      TEXT NOT NULL CHECK (security IN ('starttls', 'tls')),
    username      TEXT NOT NULL DEFAULT '',
    password      BLOB,
    from_addr     TEXT NOT NULL,
    from_name     TEXT NOT NULL DEFAULT '',
    reply_to      TEXT NOT NULL DEFAULT '',
    updated_by    TEXT NOT NULL,
    updated_at    TIMESTAMP NOT NULL,
    last_ok_at    TIMESTAMP,
    last_error    TEXT NOT NULL DEFAULT '',
    last_error_at TIMESTAMP
);

-- email_outbox is every mail Dock sends, queued until sent or given up
-- on. body is the rendered mail (JSON of its subject, text and HTML),
-- sealed and bound to nonce, since an invite's link is a secret Dock
-- otherwise keeps only as a hash; it is cleared once the mail is done.
-- ref names what the mail is about ("invite:42"). Times are Unix
-- seconds. Done rows go after 30 days (retention).
CREATE TABLE email_outbox (
    id         INTEGER PRIMARY KEY,
    kind       TEXT NOT NULL,
    org_id     INTEGER REFERENCES orgs (id) ON DELETE CASCADE,
    ref        TEXT NOT NULL DEFAULT '',
    to_addr    TEXT NOT NULL,
    subject    TEXT NOT NULL,
    nonce      TEXT NOT NULL UNIQUE,
    body       BLOB,
    status     TEXT NOT NULL CHECK (status IN ('queued', 'sent', 'failed')),
    attempts   INTEGER NOT NULL DEFAULT 0,
    next_at    INTEGER NOT NULL,
    error      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    done_at    INTEGER
);
CREATE INDEX email_outbox_due ON email_outbox (next_at) WHERE status = 'queued';
CREATE INDEX email_outbox_org_queued ON email_outbox (org_id) WHERE status = 'queued';
CREATE INDEX email_outbox_ref ON email_outbox (ref, id);
CREATE INDEX email_outbox_to ON email_outbox (to_addr, created_at);
CREATE INDEX email_outbox_done ON email_outbox (done_at) WHERE done_at IS NOT NULL;
