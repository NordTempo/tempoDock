-- Key patterns an admin marked sensitive in a key-value database (Redis,
-- ADR 0072): the keys' names and values are masked like sensitive
-- columns. Patterns are Redis globs (session:*).
CREATE TABLE sensitive_keys (
    service_id INTEGER NOT NULL REFERENCES services (id),
    pattern    TEXT NOT NULL,
    PRIMARY KEY (service_id, pattern)
) WITHOUT ROWID;
