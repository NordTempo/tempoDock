-- Storage connections (ADR 0034): named S3-compatible buckets, defined
-- once and pointed at by what keeps data in object storage. The registry
-- is the first (platform_settings.registry_storage_id, NULL: the control
-- plane's disk). credentials is the sealed JSON of the access key ID and
-- secret access key.
CREATE TABLE storage_connections (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    endpoint    TEXT NOT NULL,
    region      TEXT NOT NULL DEFAULT '',
    bucket      TEXT NOT NULL,
    path_style  BOOLEAN NOT NULL DEFAULT 1,
    credentials BLOB NOT NULL,
    created_at  TIMESTAMP NOT NULL,
    updated_at  TIMESTAMP NOT NULL,
    updated_by  TEXT NOT NULL
);

ALTER TABLE platform_settings ADD COLUMN registry_storage_id INTEGER REFERENCES storage_connections (id);
