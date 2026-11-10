-- Image vulnerability scanning (ADR 0074). One scan per image, however
-- many releases, environments or orgs run it: image_key is the registry
-- manifest digest, else the repo digest a CI image was pulled by, else
-- (no registry, one server) the local image ID. A scan is queued when a
-- release using it goes live and runs on a builder only while the
-- platform is quiet. Its SBOM is a file in the data dir (sboms/<id>.json.gz);
-- its findings replace each other per match. Times are Unix seconds.
CREATE TABLE image_scans (
    id              INTEGER PRIMARY KEY,
    image_key       TEXT NOT NULL UNIQUE,
    -- what a builder pulls when it doesn't have the image ('' for none),
    -- and a local name tried first (a build tag)
    pull_ref        TEXT NOT NULL DEFAULT '',
    local_ref       TEXT NOT NULL DEFAULT '',
    -- the builder that built it (a plain reference), preferred
    built_on_server_id INTEGER,
    -- the org whose registry credential pulls it
    org_id          INTEGER NOT NULL,
    -- a production release uses it: only builders that accept production
    -- may scan it, as only they may build it (ADR 0014 hardening)
    production      INTEGER NOT NULL DEFAULT 0,
    state           TEXT NOT NULL DEFAULT 'queued'
                    CHECK (state IN ('queued', 'scanning', 'scanned', 'failed', 'unsupported')),
    -- 0 production, 1 other live releases, 2 asked for by hand
    priority        INTEGER NOT NULL DEFAULT 1,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER,
    server_id       INTEGER,
    distro          TEXT NOT NULL DEFAULT '',
    packages        INTEGER NOT NULL DEFAULT 0,
    sbom_bytes      INTEGER NOT NULL DEFAULT 0,
    sbom_at         INTEGER,
    matched_at      INTEGER,
    db_built_at     INTEGER,
    critical        INTEGER NOT NULL DEFAULT 0,
    high            INTEGER NOT NULL DEFAULT 0,
    medium          INTEGER NOT NULL DEFAULT 0,
    low             INTEGER NOT NULL DEFAULT 0,
    other           INTEGER NOT NULL DEFAULT 0,
    fixable         INTEGER NOT NULL DEFAULT 0,
    -- known exploited (CISA KEV)
    kev             INTEGER NOT NULL DEFAULT 0,
    truncated       INTEGER NOT NULL DEFAULT 0,
    -- what fixes the worst findings: rebuild (system packages), upgrade
    -- (the app's), none, or '' with nothing critical or high
    worst_fix       TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL
);
CREATE INDEX image_scans_queue ON image_scans (priority, id) WHERE state = 'queued';

-- severity: 5 critical, 4 high, 3 medium, 2 low, 1 negligible, 0 unknown
-- (node.Severity). The key reads an image's findings worst first.
CREATE TABLE image_findings (
    scan_id       INTEGER NOT NULL REFERENCES image_scans (id) ON DELETE CASCADE,
    severity      INTEGER NOT NULL,
    vuln_id       TEXT NOT NULL,
    package       TEXT NOT NULL,
    version       TEXT NOT NULL,
    pkg_type      TEXT NOT NULL,
    fixed_in      TEXT NOT NULL DEFAULT '',
    kev           INTEGER NOT NULL DEFAULT 0,
    epss          REAL NOT NULL DEFAULT -1,
    first_seen_at INTEGER NOT NULL,
    PRIMARY KEY (scan_id, severity DESC, vuln_id, package, version)
) WITHOUT ROWID;

-- The scan of a release's image, set once it went live; a CI image's
-- repo digest, read from its server after it went live.
ALTER TABLE releases ADD COLUMN image_scan_id INTEGER;
ALTER TABLE releases ADD COLUMN pinned_ref TEXT NOT NULL DEFAULT '';
CREATE INDEX releases_image_scan ON releases (image_scan_id) WHERE image_scan_id IS NOT NULL;
CREATE INDEX releases_unscanned ON releases (id) WHERE status = 'active' AND image_scan_id IS NULL;

-- Scanning is on unless an operator turned it off.
ALTER TABLE platform_settings ADD COLUMN scanning INTEGER NOT NULL DEFAULT 1;

-- The builder's vulnerability database, for the console.
ALTER TABLE servers ADD COLUMN scan_db_built_at INTEGER;
ALTER TABLE servers ADD COLUMN scan_error TEXT NOT NULL DEFAULT '';
