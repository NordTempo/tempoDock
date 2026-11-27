-- Certificates an org uploads for its domains (ADR 0110), for names ACME
-- can't cover: Cloudflare origin certificates, an internal or corporate
-- CA. One serves every verified domain of its org that its names cover;
-- they don't renew, so Dock alerts before they expire.
--
-- names are the leaf's DNS names, lower case, separated by spaces; chain
-- is the PEM chain, leaf first; key its private key, sealed and bound to
-- (org_id, fingerprint), the leaf's SHA-256. public says whether it chains
-- to a CA the control plane trusts. not_after is when the chain stops
-- working: the leaf's end or an intermediate's, whichever is first. alerted is the last expiry alert
-- raised for it: 0 none, 1 thirty days before, 2 seven days, 3 expired.
-- IDs are never reused (AUTOINCREMENT): a removed certificate's ID never
-- names a later upload, for `dock certificate remove` or the worker.
CREATE TABLE custom_certificates (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id      INTEGER NOT NULL REFERENCES orgs (id),
    names       TEXT NOT NULL,
    chain       TEXT NOT NULL,
    key         BLOB NOT NULL,
    fingerprint TEXT NOT NULL,
    issuer      TEXT NOT NULL,
    public      BOOLEAN NOT NULL,
    not_before  TIMESTAMP NOT NULL,
    not_after   TIMESTAMP NOT NULL,
    alerted     INTEGER NOT NULL DEFAULT 0,
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMP NOT NULL,
    UNIQUE (org_id, fingerprint)
);
CREATE INDEX custom_certificates_org ON custom_certificates (org_id, id);
