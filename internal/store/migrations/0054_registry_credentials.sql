-- Pull credentials for private image registries (ADR 0051). One per
-- registry host per org: a deploy of an image from host signs in with it.
-- host is lower case, Docker Hub's aliases stored as docker.io; secret is
-- the password or token, sealed like variable values. The primary key is
-- the pull's lookup (org_id, host) and the list's order.
CREATE TABLE registry_credentials (
    org_id     INTEGER NOT NULL REFERENCES orgs (id),
    host       TEXT NOT NULL,
    username   TEXT NOT NULL,
    secret     BLOB NOT NULL,
    created_at DATETIME NOT NULL,
    created_by TEXT NOT NULL,
    updated_at DATETIME NOT NULL,
    updated_by TEXT NOT NULL,
    PRIMARY KEY (org_id, host)
) WITHOUT ROWID;
