-- This control plane's install ID, created once. Every container, network
-- and volume it creates carries it as the dock.control_plane label, so a
-- control plane never reconciles, reports or removes another one's
-- resources on a shared Docker host. Resources from before the label keep
-- working (see node.Labels.Matches).
CREATE TABLE install (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    install_id TEXT NOT NULL
);
INSERT INTO install (id, install_id) VALUES (1, lower(hex(randomblob(8))));
