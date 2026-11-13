-- More of Dock's own email (ADR 0079): password reset links and who
-- gets access request mail.
--
-- password_resets holds a reset link's code as its SHA-256 only; a link
-- works once, for an hour (core.PasswordResetTTL), and a new one replaces
-- the user's unused ones.
CREATE TABLE password_resets (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    hash       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMP NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    used_at    TIMESTAMP
);
CREATE INDEX password_resets_user ON password_resets (user_id);
CREATE INDEX password_resets_expires ON password_resets (expires_at);

-- email_requests: the user gets mail when an access request waits for
-- them to decide it; they turn it off on their Account page.
ALTER TABLE users ADD COLUMN email_requests BOOLEAN NOT NULL DEFAULT 1;
