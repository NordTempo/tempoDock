-- The slugs an org had before it changed its slug (ADR 0064), so old
-- addresses keep working: the API's `org` parameter, web links and CLI
-- contexts that name one resolve it to the org. A slug is here at most
-- once and never also an org's current slug: an org taking it, or the
-- org setting it again, removes it.
CREATE TABLE org_old_slugs (
    slug       TEXT PRIMARY KEY,
    org_id     INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    changed_at DATETIME NOT NULL
) WITHOUT ROWID;
CREATE INDEX org_old_slugs_org ON org_old_slugs (org_id);
