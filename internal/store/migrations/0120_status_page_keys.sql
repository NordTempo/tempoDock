-- Status page addresses nobody can guess (ADR 0087): a page's name is
-- unique in its organization only, and its address carries a random
-- part, status-<slug>-<address_key>.<base domain>. Pages from before keep
-- address_key '' and so their address, until they are renamed.
ALTER TABLE status_pages ADD COLUMN address_key TEXT NOT NULL DEFAULT '';
DROP INDEX status_pages_slug;
DROP INDEX status_pages_org;
CREATE UNIQUE INDEX status_pages_org_slug ON status_pages (org_id, slug);

-- Old names are an organization's too, each with the random part the
-- page's address had under it.
CREATE TABLE status_page_old_slugs_new (
    org_id      INTEGER NOT NULL,
    slug        TEXT NOT NULL,
    page_id     INTEGER NOT NULL REFERENCES status_pages (id) ON DELETE CASCADE,
    address_key TEXT NOT NULL DEFAULT '',
    changed_at  DATETIME NOT NULL,
    PRIMARY KEY (org_id, slug)
) WITHOUT ROWID;
INSERT INTO status_page_old_slugs_new (org_id, slug, page_id, changed_at)
SELECT p.org_id, o.slug, o.page_id, o.changed_at
FROM status_page_old_slugs o JOIN status_pages p ON p.id = o.page_id;
DROP TABLE status_page_old_slugs;
ALTER TABLE status_page_old_slugs_new RENAME TO status_page_old_slugs;
CREATE INDEX status_page_old_slugs_page ON status_page_old_slugs (page_id, changed_at);
