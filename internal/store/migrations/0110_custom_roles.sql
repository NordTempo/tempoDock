-- An org's own roles (ADR 0080, phase 5): built-in roles and single
-- permissions under one name, granted like a built-in role. parts lists
-- them space-separated ("deploy:edit variables:edit errors.manage"), read
-- with the grants that name the role, so what a built-in role holds in a
-- later Dock reaches the roles that include it.
CREATE TABLE roles (
    id          INTEGER PRIMARY KEY,
    org_id      INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    parts       TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, name)
);

-- A grant names a built-in role (role) or one of the org's (role_id, with
-- role ''). A role in use can't be removed.
ALTER TABLE grants ADD COLUMN role_id INTEGER REFERENCES roles (id);
CREATE INDEX grants_role ON grants (role_id) WHERE role_id IS NOT NULL;
