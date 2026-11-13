-- The names a status page had before it was renamed (ADR 0073,
-- renaming): API lookups by an old name find the page, and its old
-- generated address answers 301 to the page's address. A name is here at
-- most once and never also a page's current name: a page taking it
-- removes it. Each page keeps its newest few (statusOldSlugs).
CREATE TABLE status_page_old_slugs (
    slug       TEXT PRIMARY KEY,
    page_id    INTEGER NOT NULL REFERENCES status_pages (id) ON DELETE CASCADE,
    changed_at DATETIME NOT NULL
) WITHOUT ROWID;
CREATE INDEX status_page_old_slugs_page ON status_page_old_slugs (page_id, changed_at);
