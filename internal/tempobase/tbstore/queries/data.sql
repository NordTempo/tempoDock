-- The data panel's records per connection (datapanel.Host).

-- name: GetDataAccess :one
SELECT * FROM data_access WHERE connection_id = +?;

-- name: UpsertDataAccess :exec
-- Records a setup or rotation; the grants are resynced afterwards.
INSERT INTO data_access (connection_id, engine, server_version, reader_secret, writer_secret, grants_synced_hash, set_up_at, set_up_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (connection_id) DO UPDATE SET
    engine = excluded.engine, server_version = excluded.server_version,
    reader_secret = excluded.reader_secret, writer_secret = excluded.writer_secret,
    grants_synced_hash = excluded.grants_synced_hash, set_up_at = excluded.set_up_at, set_up_by = excluded.set_up_by;

-- name: SetGrantsSyncedHash :exec
UPDATE data_access SET grants_synced_hash = ? WHERE connection_id = +?;

-- name: ListSensitiveColumns :many
SELECT schema_name, table_name, column_name FROM sensitive_columns
WHERE connection_id = +?
ORDER BY schema_name, table_name, column_name;

-- name: DeleteSensitiveColumns :exec
DELETE FROM sensitive_columns WHERE connection_id = +?;

-- name: AddSensitiveColumn :exec
INSERT INTO sensitive_columns (connection_id, schema_name, table_name, column_name) VALUES (?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: ListSavedQueries :many
SELECT * FROM saved_queries
WHERE connection_id = +sqlc.arg(connection_id) AND user_id = +sqlc.arg(user_id)
ORDER BY name;

-- name: SaveQuery :one
-- Creates a saved query, or replaces the SQL of the user's one with that
-- name.
INSERT INTO saved_queries (connection_id, user_id, name, sql, created_at, updated_at)
VALUES (sqlc.arg(connection_id), sqlc.arg(user_id), sqlc.arg(name), sqlc.arg(sql), sqlc.arg(now), sqlc.arg(now))
ON CONFLICT (connection_id, user_id, name) DO UPDATE SET sql = excluded.sql, updated_at = excluded.updated_at
RETURNING *;

-- name: UpdateSavedQuery :one
-- No row when it isn't the user's on this connection; taking another
-- query's name is a unique violation.
UPDATE saved_queries SET name = sqlc.arg(name), sql = sqlc.arg(sql), updated_at = sqlc.arg(now)
WHERE id = +sqlc.arg(id) AND connection_id = +sqlc.arg(connection_id) AND user_id = +sqlc.arg(user_id)
RETURNING *;

-- name: DeleteSavedQuery :execrows
DELETE FROM saved_queries
WHERE id = +sqlc.arg(id) AND connection_id = +sqlc.arg(connection_id) AND user_id = +sqlc.arg(user_id);

-- name: CreateChangeset :one
INSERT INTO changesets (connection_id, author, author_kind, user_id, token_id, message, changes, sql, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetChangeset :one
SELECT * FROM changesets WHERE id = +sqlc.arg(id) AND connection_id = +sqlc.arg(connection_id);

-- name: ListChangesets :many
-- A connection's latest changesets in any status, newest first.
SELECT * FROM changesets
WHERE connection_id = +sqlc.arg(connection_id)
ORDER BY id DESC
LIMIT +sqlc.arg(max_rows);

-- name: ListPendingChangesets :many
-- A connection's pending changesets, newest first. Run ExpireChangesets
-- first.
SELECT * FROM changesets
WHERE connection_id = +sqlc.arg(connection_id) AND status = 'pending'
ORDER BY id DESC;

-- name: DecideChangeset :one
-- Ends a pending changeset that hasn't expired by decided_at; no row
-- means someone decided first or it expired.
UPDATE changesets SET
    status = sqlc.arg(status), decided_at = sqlc.arg(decided_at), decided_by = sqlc.arg(decided_by),
    reason = sqlc.arg(reason), rows_affected = sqlc.arg(rows_affected), conflicts = sqlc.arg(conflicts)
WHERE id = +sqlc.arg(id) AND connection_id = +sqlc.arg(connection_id)
  AND status = 'pending' AND expires_at > +sqlc.arg(decided_at)
RETURNING *;

-- name: ExpireChangesets :exec
-- Run lazily before changesets are read; nothing runs it on a timer.
UPDATE changesets SET status = 'expired', decided_at = expires_at
WHERE status = 'pending' AND expires_at <= +?;
