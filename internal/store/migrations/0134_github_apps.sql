-- foreign_keys: off
-- GitHub Apps of the platform or an org (ADR 0105). github_app held the
-- install's one App; github_apps holds any number, each the platform's
-- (org_id NULL) or one org's. The existing App becomes the platform's,
-- with ID 1. An installation names the App it is of; GitHub's
-- installation IDs are unique across Apps, so they stay the key.
--
-- The migrated App's sealed values were bound to the one-row table;
-- Dock binds them to their row on its next start (resealGitHubApps).
CREATE TABLE github_apps (
    id             INTEGER PRIMARY KEY,
    org_id         INTEGER REFERENCES orgs (id) ON DELETE CASCADE,
    app_id         INTEGER NOT NULL UNIQUE,
    hook_id        TEXT NOT NULL UNIQUE,
    slug           TEXT NOT NULL UNIQUE,
    name           TEXT NOT NULL,
    owner          TEXT NOT NULL,
    -- owner_id is the owner's GitHub account ID: an org's App counts only
    -- installations on that account. 0 for the migrated App, which is the
    -- platform's and doesn't need it.
    owner_id       INTEGER NOT NULL DEFAULT 0,
    html_url       TEXT NOT NULL,
    private_key    BLOB NOT NULL,
    webhook_secret BLOB NOT NULL,
    client_id      TEXT NOT NULL DEFAULT '',
    client_secret  BLOB,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMP NOT NULL
);
CREATE INDEX github_apps_org ON github_apps (org_id);

INSERT INTO github_apps (id, org_id, app_id, hook_id, slug, name, owner, html_url, private_key, webhook_secret,
                         client_id, client_secret, created_by, created_at)
SELECT 1, NULL, app_id, hook_id, slug, name, owner, html_url, private_key, webhook_secret,
       client_id, client_secret, created_by, created_at
FROM github_app;
DROP TABLE github_app;

CREATE TABLE github_installations_new (
    installation_id INTEGER PRIMARY KEY,
    app_id          INTEGER NOT NULL REFERENCES github_apps (id) ON DELETE CASCADE,
    org_id          INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    account         TEXT NOT NULL,
    assigned_by     TEXT NOT NULL,
    assigned_at     TIMESTAMP NOT NULL
);
INSERT INTO github_installations_new (installation_id, app_id, org_id, account, assigned_by, assigned_at)
SELECT installation_id, 1, org_id, account, assigned_by, assigned_at
FROM github_installations WHERE EXISTS (SELECT 1 FROM github_apps WHERE id = 1);
DROP TABLE github_installations;
ALTER TABLE github_installations_new RENAME TO github_installations;
CREATE INDEX github_installations_org ON github_installations (org_id);
CREATE INDEX github_installations_app ON github_installations (app_id);

-- The App's alert is now one per App.
UPDATE alerts SET key = 'github:app:1' WHERE key = 'github:app';
