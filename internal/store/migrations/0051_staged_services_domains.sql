-- Staged structural changes for project files (`dock apply FILE`, ADR 0049).
-- A service can be staged for creation (it exists as a row so variables and
-- volumes can be staged on it, but runs nothing until applied) or for
-- archiving. Domains can be staged for adding or removal.
ALTER TABLE services ADD COLUMN staged TEXT NOT NULL DEFAULT ''
    CHECK (staged IN ('', 'create', 'archive'));

-- A staged domain change of a service. hostname is '' for a generated
-- domain still to be generated.
CREATE TABLE staged_domains (
    id          INTEGER PRIMARY KEY,
    service_id  INTEGER NOT NULL REFERENCES services (id),
    hostname    TEXT NOT NULL,
    action      TEXT NOT NULL CHECK (action IN ('add', 'remove')),
    generated   BOOLEAN NOT NULL DEFAULT 0,
    redirect_to TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (service_id, hostname)
);
