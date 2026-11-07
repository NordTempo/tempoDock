-- Error issues (ADR 0062, "One place for errors"): a service's errors
-- grouped by fingerprint, from its containers' logs, Dock Analytics'
-- frontend errors and 5xx spikes in its traffic. Written once a minute on
-- the statistics tick. org_id, project_id and environment_id are the
-- service's, which never move, so an org's list is one index range.
CREATE TABLE error_issues (
    id             INTEGER PRIMARY KEY,
    org_id         INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    project_id     INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    environment_id INTEGER NOT NULL REFERENCES environments (id) ON DELETE CASCADE,
    service_id     INTEGER NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    fingerprint    TEXT NOT NULL,
    -- logs, frontend or traffic.
    source         TEXT NOT NULL,
    title          TEXT NOT NULL,
    culprit        TEXT NOT NULL DEFAULT '',
    -- The latest occurrence: a stack trace, a log excerpt or the 5xx
    -- counts, at most 8 KB, secret values masked.
    sample         TEXT NOT NULL DEFAULT '',
    count          INTEGER NOT NULL,
    first_seen     DATETIME NOT NULL,
    last_seen      DATETIME NOT NULL,
    -- The service's release numbers it was first and last seen in; 0
    -- when not known.
    first_release  INTEGER NOT NULL DEFAULT 0,
    last_release   INTEGER NOT NULL DEFAULT 0,
    -- Resolved by a person (resolved_by), or by itself after 7 days
    -- without an occurrence (resolved_by '').
    resolved_at    DATETIME,
    resolved_by    TEXT NOT NULL DEFAULT '',
    -- When it was seen again after being resolved; cleared when it is
    -- resolved again.
    regressed_at   DATETIME,
    UNIQUE (service_id, fingerprint)
);
-- The errors page: an org's, a project's or a service's issues, last
-- seen first; and the org's issues seen since a time, for trends.
CREATE INDEX error_issues_org ON error_issues (org_id, last_seen);
CREATE INDEX error_issues_project ON error_issues (project_id, last_seen);
CREATE INDEX error_issues_service ON error_issues (service_id, last_seen);
-- The page's facets: open issues per service.
CREATE INDEX error_issues_open ON error_issues (org_id, service_id) WHERE resolved_at IS NULL;
-- The retention pass: open issues gone quiet, resolved ones long gone.
CREATE INDEX error_issues_quiet ON error_issues (last_seen) WHERE resolved_at IS NULL;
CREATE INDEX error_issues_resolved ON error_issues (last_seen) WHERE resolved_at IS NOT NULL;
-- "N new errors since this release".
CREATE INDEX error_issues_first_release ON error_issues (service_id, first_release);

-- Each issue's errors per hour (Unix seconds at its start), for its
-- 24-hour trend; the retention pass keeps 7 days.
CREATE TABLE error_counts (
    issue_id INTEGER NOT NULL REFERENCES error_issues (id) ON DELETE CASCADE,
    hour     INTEGER NOT NULL,
    count    INTEGER NOT NULL,
    PRIMARY KEY (issue_id, hour)
) WITHOUT ROWID;
CREATE INDEX error_counts_hour ON error_counts (hour);
