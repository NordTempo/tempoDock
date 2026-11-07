-- The platform sets defaults and limits, orgs decide within them (ADR
-- 0062). platform_data_policy holds the platform's data access default
-- (rule 'default': what applies where an org set nothing, in place of
-- the built-in one) and ceiling (rule 'ceiling': the most an org's rule
-- or token gives) per subject and environment kind. No rows: the built-in
-- default and no ceiling, as before.
CREATE TABLE platform_data_policy (
    rule      TEXT NOT NULL CHECK (rule IN ('default', 'ceiling')),
    subject   TEXT NOT NULL,
    kind      TEXT NOT NULL,
    level     TEXT NOT NULL,
    sensitive TEXT NOT NULL,
    PRIMARY KEY (rule, subject, kind)
) WITHOUT ROWID;

-- An org's defaults for its new analytics sites (sites.settings' shape),
-- over the platform's (analytics_settings.site_defaults).
CREATE TABLE org_site_defaults (
    org_id     INTEGER PRIMARY KEY REFERENCES orgs (id) ON DELETE CASCADE,
    settings   TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    updated_at DATETIME NOT NULL
);
