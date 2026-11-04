-- Two-factor sign-in (ADR 0049): passkeys and authenticator apps as second
-- factors, one-time recovery codes, what each session proved, and the org
-- and platform settings that require a second factor.

-- A user's second factors. A passkey's credential_id is its WebAuthn
-- credential ID and secret the sealed webauthn.Credential (JSON, with the
-- public key and sign count). An authenticator app's (totp) secret is its
-- sealed key, and last_step the last time step accepted, so each code
-- works once.
CREATE TABLE user_factors (
    id            INTEGER PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('passkey', 'totp')),
    name          TEXT NOT NULL,
    credential_id BLOB,
    secret        BLOB NOT NULL,
    last_step     INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL,
    last_used_at  DATETIME
);
CREATE INDEX user_factors_user ON user_factors (user_id);
CREATE UNIQUE INDEX user_factors_credential ON user_factors (credential_id) WHERE credential_id IS NOT NULL;
CREATE UNIQUE INDEX user_factors_one_totp ON user_factors (user_id) WHERE kind = 'totp';

-- One-time recovery codes: the SHA-256 of each, used_at once spent.
CREATE TABLE recovery_codes (
    user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    hash    TEXT NOT NULL,
    used_at DATETIME,
    PRIMARY KEY (user_id, hash)
) WITHOUT ROWID;

-- factor is the second factor the session's login used ('' for none,
-- passkey, totp or recovery). A pending session has had its password
-- checked but not its second factor yet, and authenticates nothing;
-- attempts counts its wrong second factors. verified_at is when the person
-- last proved who they are (signing in or a step-up), for sensitive
-- actions.
ALTER TABLE sessions ADD COLUMN factor TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN pending BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN verified_at DATETIME;

-- A `dock login` approved from a session that used a second factor; its
-- token counts as two-factor where an org requires it.
ALTER TABLE cli_logins ADD COLUMN two_factor BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE tokens ADD COLUMN two_factor BOOLEAN NOT NULL DEFAULT 0;

-- Members act in the org only with a second factor.
ALTER TABLE orgs ADD COLUMN require_two_factor BOOLEAN NOT NULL DEFAULT 0;
-- Operators run the platform only with a second factor.
ALTER TABLE platform_settings ADD COLUMN operators_two_factor BOOLEAN NOT NULL DEFAULT 0;
