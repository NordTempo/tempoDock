-- An organization's own app templates (ADR 0083): the template file as
-- its members save it, in the catalog's format.
CREATE TABLE org_templates (
    id         INTEGER PRIMARY KEY,
    org_id     INTEGER NOT NULL REFERENCES orgs (id),
    name       TEXT NOT NULL,
    body       TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    UNIQUE (org_id, name)
);
