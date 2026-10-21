-- Browser logins for the CLI (`dock login`, device authorization style):
-- the CLI starts one and long-polls with its device code; a signed-in user
-- approves or denies it in the UI by its user code; the CLI then collects
-- a fresh API token and the row is deleted. Rows live for minutes.
CREATE TABLE cli_logins (
    id          INTEGER PRIMARY KEY,
    -- SHA-256 of the device code, the CLI's secret for collecting the token.
    device_hash TEXT NOT NULL UNIQUE,
    -- What the user compares with the terminal, stored without the dash.
    user_code   TEXT NOT NULL UNIQUE,
    client_name TEXT NOT NULL,
    hostname    TEXT NOT NULL,
    os          TEXT NOT NULL,
    ip          TEXT NOT NULL,
    created_at  DATETIME NOT NULL,
    expires_at  DATETIME NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied')),
    -- The user who approved or denied it; the token is theirs.
    user_id     INTEGER REFERENCES users (id) ON DELETE CASCADE
);

-- Where a token came from: created by an admin (or bootstrap), or by a
-- user approving `dock login` in the browser.
ALTER TABLE tokens ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual' CHECK (origin IN ('manual', 'cli_login'));
