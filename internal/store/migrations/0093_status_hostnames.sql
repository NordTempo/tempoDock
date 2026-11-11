-- Custom hostnames for status pages (ADR 0073, phase 4): a page may also
-- answer on a hostname of its org's, once it proves control of it with
-- a TXT record (ADR 0042) or Dock makes its record in a connected
-- Cloudflare zone (ADR 0036). Unverified claims don't block each other;
-- at most one page holds a hostname verified.
ALTER TABLE status_pages ADD COLUMN hostname TEXT;
ALTER TABLE status_pages ADD COLUMN verify_token TEXT NOT NULL DEFAULT '';
ALTER TABLE status_pages ADD COLUMN verified_at TIMESTAMP;
CREATE INDEX status_pages_hostname ON status_pages (hostname) WHERE hostname IS NOT NULL;
CREATE UNIQUE INDEX status_pages_hostname_verified ON status_pages (hostname) WHERE verified_at IS NOT NULL;

-- A record Dock makes for a page's hostname, through the control plane's
-- tunnel like a domain's.
ALTER TABLE cloudflare_records ADD COLUMN status_page_id INTEGER REFERENCES status_pages (id) ON DELETE SET NULL;
CREATE INDEX cloudflare_records_status_page ON cloudflare_records (status_page_id) WHERE status_page_id IS NOT NULL;
