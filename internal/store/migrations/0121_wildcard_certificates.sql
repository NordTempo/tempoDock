-- Wildcard certificates for base domains (ADR 0090): Dock obtains
-- *.<base> and <base> over ACME DNS-01 through the platform's Cloudflare
-- connection, so generated hostnames never reach certificate-transparency
-- logs. One row per base domain Dock has tried to get one for.
--
-- chain is the PEM chain and key its sealed private key, both empty until
-- the first issuance; not_after is NULL until then. error is the last
-- attempt's failure ('' after a success) with attempts the failures since
-- the last success; retry_at is when to try again, NULL when nothing is
-- due on a timer (issued, or waiting for the token to change).
CREATE TABLE wildcard_certificates (
    base_domain TEXT PRIMARY KEY,
    chain       TEXT NOT NULL DEFAULT '',
    key         BLOB NOT NULL DEFAULT x'',
    not_before  TIMESTAMP,
    not_after   TIMESTAMP,
    error       TEXT NOT NULL DEFAULT '',
    attempts    INTEGER NOT NULL DEFAULT 0,
    retry_at    TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL
);

-- The ACME account Dock orders with, one per CA directory: the account is
-- its key (sealed).
CREATE TABLE acme_accounts (
    directory  TEXT PRIMARY KEY,
    key        BLOB NOT NULL,
    created_at TIMESTAMP NOT NULL
);
