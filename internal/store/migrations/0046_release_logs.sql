-- Log history (ADR 0046): when dock removes a release's container, its
-- output is archived on the server that ran it. logs_server_id is that
-- server, so a release's logs are found after its environment moved. A
-- plain reference like built_on_server_id: a server that ran releases
-- can still be removed. NULL: no archive (none yet, an older node, or a
-- release from before this).
ALTER TABLE releases ADD COLUMN logs_server_id INTEGER;
