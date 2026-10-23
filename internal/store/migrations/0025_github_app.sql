-- The install's GitHub App (ADR 0030): one row (id 1), registered through
-- GitHub's manifest flow. private_key and webhook_secret are sealed with
-- the secrets box. hook_id is its webhook URL's last segment, random like
-- a connection's, so only GitHub knows where to deliver.
CREATE TABLE github_app (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    app_id         INTEGER NOT NULL,
    hook_id        TEXT NOT NULL,
    slug           TEXT NOT NULL,
    name           TEXT NOT NULL,
    owner          TEXT NOT NULL,
    html_url       TEXT NOT NULL,
    private_key    BLOB NOT NULL,
    webhook_secret BLOB NOT NULL,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMP NOT NULL
);

-- Which org may deploy from each of the App's installations. An operator
-- assigns them; a GitHub account's repositories are only ever readable by
-- the org its installation is assigned to.
CREATE TABLE github_installations (
    installation_id INTEGER PRIMARY KEY,
    org_id          INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    account         TEXT NOT NULL,
    assigned_by     TEXT NOT NULL,
    assigned_at     TIMESTAMP NOT NULL
);
CREATE INDEX github_installations_org ON github_installations (org_id);

-- Connections through the App: the installation that reads the
-- repository and the repository's GitHub ID, which App deliveries are
-- routed by (it survives renames). Both 0 for webhook connections. The
-- index also counts connections per installation for the console.
ALTER TABLE git_connections ADD COLUMN installation_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE git_connections ADD COLUMN repo_id INTEGER NOT NULL DEFAULT 0;
CREATE INDEX git_connections_app ON git_connections (installation_id, repo_id);
