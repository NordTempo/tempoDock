-- Dock Analytics' goals and funnels (ADR 0037, phase A4). A goal is a
-- page (a path pattern, * matching any run of characters) or a custom
-- event (its name) that counts as a conversion; a funnel is two to eight
-- such steps a session takes in order, kept as JSON
-- ([{"kind": "page", "match": "/pricing"}, ...]). The analytics store
-- counts them; Dock keeps what they are.
CREATE TABLE site_goals (
    id         INTEGER PRIMARY KEY,
    site_id    INTEGER NOT NULL REFERENCES sites (id),
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('page', 'event')),
    match      TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);
CREATE UNIQUE INDEX site_goals_name ON site_goals (site_id, name);

CREATE TABLE site_funnels (
    id         INTEGER PRIMARY KEY,
    site_id    INTEGER NOT NULL REFERENCES sites (id),
    name       TEXT NOT NULL,
    steps      TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);
CREATE UNIQUE INDEX site_funnels_name ON site_funnels (site_id, name);
