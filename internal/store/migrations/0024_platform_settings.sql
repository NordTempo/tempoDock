-- Platform-wide settings operators change from the console (ADR 0029):
-- one row (id 1), created on first change. public_host is Dock's public
-- address, served by the control plane's Caddy; '' when there is none.
CREATE TABLE platform_settings (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    public_host TEXT NOT NULL DEFAULT '',
    updated_by  TEXT NOT NULL,
    updated_at  TIMESTAMP NOT NULL
);
