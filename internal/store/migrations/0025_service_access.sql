-- Access protection for a service's domains (ADR 0030). A service without
-- a row is public. mode 'password' asks for HTTP basic auth with username
-- and password_hash (bcrypt); 'login' asks for a Dock login, with tokens
-- signed by key (sealed with internal/secrets).
CREATE TABLE service_access (
    service_id    INTEGER PRIMARY KEY REFERENCES services (id),
    mode          TEXT NOT NULL CHECK (mode IN ('password', 'login')),
    username      TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',
    key           BLOB,
    updated_by    TEXT NOT NULL,
    updated_at    TIMESTAMP NOT NULL
);
