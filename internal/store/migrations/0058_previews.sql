-- Preview environments per pull request (ADR 0056).
--
-- preview_settings is a project's opt-in: pull requests to a repository
-- the base environment's services deploy get an environment cloned from
-- it. copy_variables is a JSON list of the base's variables
-- ("web.API_URL", "shared.SENTRY_DSN") whose literal values a preview
-- may copy; no other literal value ever reaches a preview.
CREATE TABLE preview_settings (
    project_id          INTEGER PRIMARY KEY REFERENCES projects (id),
    base_environment_id INTEGER NOT NULL REFERENCES environments (id),
    max_previews        INTEGER NOT NULL,
    copy_variables      TEXT NOT NULL DEFAULT '[]',
    allow_forks         BOOLEAN NOT NULL DEFAULT 0,
    updated_by          TEXT NOT NULL,
    updated_at          DATETIME NOT NULL
);
CREATE UNIQUE INDEX preview_settings_base ON preview_settings (base_environment_id);

-- previews is one pull request's preview environment, open or closed
-- (closed_at), cloned from base_environment_id. repo is the base
-- connection's repository, as Dock names it. pr_updated_at is the pull request's updated_at in the newest event
-- applied: an older event (a late or replayed delivery) changes nothing.
-- comment_id is the one pull request comment Dock edits in place (0:
-- none yet).
CREATE TABLE previews (
    id             INTEGER PRIMARY KEY,
    project_id     INTEGER NOT NULL REFERENCES projects (id),
    environment_id INTEGER NOT NULL UNIQUE REFERENCES environments (id),
    base_environment_id INTEGER NOT NULL REFERENCES environments (id),
    repo           TEXT NOT NULL,
    number         INTEGER NOT NULL,
    title          TEXT NOT NULL DEFAULT '',
    author         TEXT NOT NULL DEFAULT '',
    html_url       TEXT NOT NULL DEFAULT '',
    head_branch    TEXT NOT NULL,
    head_sha       TEXT NOT NULL,
    fork           BOOLEAN NOT NULL DEFAULT 0,
    pr_updated_at  DATETIME NOT NULL,
    comment_id     INTEGER NOT NULL DEFAULT 0,
    last_event     TEXT NOT NULL DEFAULT '',
    created_at     DATETIME NOT NULL,
    updated_at     DATETIME NOT NULL,
    closed_at      DATETIME
);
-- One open preview per pull request; the open ones of a project count
-- against its cap.
CREATE UNIQUE INDEX previews_open ON previews (project_id, repo, number) WHERE closed_at IS NULL;
-- The newest closed preview of a pull request, whose pr_updated_at stops
-- a late event from reopening it.
CREATE INDEX previews_pull ON previews (project_id, repo, number, closed_at);

-- An archived environment (a closed preview) is hidden from lists. It is
-- renamed on archive ("pr-12~34"), so its name is free for a new one.
ALTER TABLE environments ADD COLUMN archived_at DATETIME;
