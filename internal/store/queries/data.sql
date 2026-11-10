-- Database panel (ADR 0016). Parameters compared to indexed columns are
-- written `= +?` and limits `LIMIT +?` (docs/performance.md, the re-prepare
-- pitfall); scope IDs use 0 for "none".

-- name: SetServiceDatabaseSettings :one
-- Sets a service's database and database_url_variable settings. They apply
-- immediately: no release, nothing staged.
UPDATE services SET database = sqlc.arg(database), database_url_variable = sqlc.arg(database_url_variable)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetTokenDataAccess :execrows
-- Sets a token's own data access; NULLs for all three make it follow the
-- policy's tokens rule again.
UPDATE tokens SET
    data_production = sqlc.narg(data_production),
    data_non_production = sqlc.narg(data_non_production),
    data_sensitive = sqlc.narg(data_sensitive)
WHERE id = sqlc.arg(id);

-- name: ListDataPolicyRules :many
-- The rules set at exactly one scope: project_id and environment_id 0 for
-- the default, environment_id 0 for a project.
SELECT * FROM data_policy_rules
WHERE org_id = +sqlc.arg(org_id)
  AND coalesce(project_id, 0) = +CAST(sqlc.arg(project_id) AS INTEGER)
  AND coalesce(environment_id, 0) = +CAST(sqlc.arg(environment_id) AS INTEGER)
ORDER BY subject, kind;

-- name: ListEffectiveDataPolicyRules :many
-- Every rule that can apply in one environment (or, with environment_id 0,
-- one project): the default's, the project's and the environment's, every
-- subject and kind. Per subject and kind the most specific wins: core picks
-- the row with environment_id set, then project_id set, then the default,
-- then the built-in defaults. Unordered: at most 18 rows.
SELECT * FROM data_policy_rules
WHERE org_id = +sqlc.arg(org_id)
  AND coalesce(project_id, 0) IN (0, +CAST(sqlc.arg(project_id) AS INTEGER))
  AND coalesce(environment_id, 0) IN (0, +CAST(sqlc.arg(environment_id) AS INTEGER));

-- name: DeleteDataPolicyRules :exec
-- Removes the rules at one scope (see ListDataPolicyRules); replacing a
-- scope's policy is this plus AddDataPolicyRule per rule in one transaction.
DELETE FROM data_policy_rules
WHERE org_id = +sqlc.arg(org_id)
  AND coalesce(project_id, 0) = +CAST(sqlc.arg(project_id) AS INTEGER)
  AND coalesce(environment_id, 0) = +CAST(sqlc.arg(environment_id) AS INTEGER);

-- name: AddDataPolicyRule :exec
-- NULL project_id and environment_id add to the default policy; a second
-- rule for the same scope, subject and kind is a unique violation.
INSERT INTO data_policy_rules (org_id, project_id, environment_id, subject, kind, level, sensitive)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetDataAccess :one
SELECT * FROM data_access WHERE service_id = ?;

-- name: UpsertDataAccess :one
-- Records a setup or rotation: new sealed role passwords and who did it.
-- The roles' grants are resynced afterwards, so the hash resets.
INSERT INTO data_access (service_id, engine, server_version, reader_secret, writer_secret, set_up_at, set_up_by)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (service_id) DO UPDATE SET
    engine = excluded.engine, server_version = excluded.server_version,
    reader_secret = excluded.reader_secret, writer_secret = excluded.writer_secret,
    grants_synced_hash = '', set_up_at = excluded.set_up_at, set_up_by = excluded.set_up_by
RETURNING *;

-- name: SetDataGrantsSyncedHash :exec
UPDATE data_access SET grants_synced_hash = ? WHERE service_id = ?;

-- name: ListSensitiveColumns :many
SELECT schema_name, table_name, column_name FROM sensitive_columns
WHERE service_id = +sqlc.arg(service_id)
ORDER BY schema_name, table_name, column_name;

-- name: DeleteSensitiveColumns :exec
-- Clears a database's marks; replacing them is this plus AddSensitiveColumn
-- per column in one transaction.
DELETE FROM sensitive_columns WHERE service_id = +sqlc.arg(service_id);

-- name: AddSensitiveColumn :exec
INSERT INTO sensitive_columns (service_id, schema_name, table_name, column_name) VALUES (?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: ListSavedQueries :many
-- One user's saved queries on one database, by name.
SELECT * FROM saved_queries
WHERE service_id = +sqlc.arg(service_id) AND user_id = +sqlc.arg(user_id)
ORDER BY name;

-- name: SaveQuery :one
-- Creates a saved query, or replaces the SQL of the user's one with that
-- name.
INSERT INTO saved_queries (service_id, user_id, name, sql, created_at, updated_at)
VALUES (sqlc.arg(service_id), sqlc.arg(user_id), sqlc.arg(name), sqlc.arg(sql), sqlc.arg(now), sqlc.arg(now))
ON CONFLICT (service_id, user_id, name) DO UPDATE SET sql = excluded.sql, updated_at = excluded.updated_at
RETURNING *;

-- name: UpdateSavedQuery :one
-- Renames or edits one of the user's saved queries; no row (IsNotFound) if
-- it isn't theirs on this database. Taking another query's name is a unique
-- violation.
UPDATE saved_queries SET name = sqlc.arg(name), sql = sqlc.arg(sql), updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND service_id = +sqlc.arg(service_id) AND user_id = +sqlc.arg(user_id)
RETURNING *;

-- name: DeleteSavedQuery :execrows
DELETE FROM saved_queries
WHERE id = sqlc.arg(id) AND service_id = +sqlc.arg(service_id) AND user_id = +sqlc.arg(user_id);

-- name: CreateChangeset :one
-- Stores a changeset as pending. Committing one's own goes through
-- DecideChangeset like an approval.
INSERT INTO changesets (service_id, author, author_kind, user_id, token_id, message, changes, key_changes, sql, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetChangeset :one
SELECT * FROM changesets WHERE id = sqlc.arg(id) AND service_id = +sqlc.arg(service_id);

-- name: ListChangesets :many
-- One database's changesets, newest first, with IDs below before_id (pass
-- math.MaxInt64 for the newest).
SELECT * FROM changesets
WHERE service_id = +sqlc.arg(service_id) AND id < sqlc.arg(before_id)
ORDER BY id DESC
LIMIT +sqlc.arg(max_rows);

-- name: ListPendingChangesets :many
-- One database's pending changesets, oldest first. Run ExpireChangesets
-- first so none past expires_at show up.
SELECT * FROM changesets
WHERE service_id = +sqlc.arg(service_id) AND status = 'pending'
ORDER BY id;

-- name: DecideChangeset :one
-- Ends a pending changeset: committed (with rows_affected), conflict (with
-- conflicts) or rejected (with reason). Only a changeset still pending and
-- not expired by decided_at changes, so concurrent decisions can't both
-- win: no row (IsNotFound) means someone else decided first or it expired.
UPDATE changesets SET
    status = sqlc.arg(status), decided_at = sqlc.arg(decided_at), decided_by = sqlc.arg(decided_by),
    reason = sqlc.arg(reason), rows_affected = sqlc.arg(rows_affected), conflicts = sqlc.arg(conflicts)
WHERE id = sqlc.arg(id) AND status = 'pending' AND expires_at > +sqlc.arg(decided_at)
RETURNING *;

-- name: ExpireChangesets :execrows
-- Marks every pending changeset past its expiry as expired, decided when
-- it expired. Run lazily before changesets are listed; nothing runs it on
-- a timer.
UPDATE changesets SET status = 'expired', decided_at = expires_at
WHERE status = 'pending' AND expires_at <= +sqlc.arg(now);

-- name: AddAuditDetail :one
-- The detail row of a data audit event. The caller cuts sql at
-- apitypes.AuditSQLMaxBytes.
INSERT INTO audit_details (sql, rows, duration_ms, outcome) VALUES (?, ?, ?, ?)
RETURNING id;

-- name: AddAuditEventWithDetail :one
-- AddAuditEvent with a detail row (AddAuditDetail, in the same
-- transaction).
INSERT INTO audit_events (actor, actor_kind, user_id, token_id, action, org_id, project_id, environment_id, target, summary, created_at, detail_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAuditDetail :one
SELECT * FROM audit_details WHERE id = ?;

-- name: GetAuditEventByDetail :one
-- The event a detail belongs to, to check who may read the detail.
SELECT * FROM audit_events WHERE detail_id = +CAST(sqlc.arg(detail_id) AS INTEGER);

-- name: ListDataQueryHistory :many
-- One actor's queries on one database (target "project/environment/service"),
-- newest first, from their data.query audit events: user_id for a user,
-- token_id for a token, the other NULL. Pages back with before_id (pass
-- math.MaxInt64 for the newest). The stable environment and target suffix keep
-- this history available after a project rename, isolated from other orgs.
SELECT audit_events.id AS audit_id, audit_events.created_at, sqlc.embed(audit_details)
FROM audit_events
JOIN audit_details ON audit_details.id = audit_events.detail_id
WHERE audit_events.action = 'data.query'
  AND audit_events.environment_id = +sqlc.arg(environment_id)
  AND substr(audit_events.target, instr(audit_events.target, '/') + 1) = +sqlc.arg(target)
  AND audit_events.user_id IS +CAST(sqlc.narg(user_id) AS INTEGER)
  AND audit_events.token_id IS +CAST(sqlc.narg(token_id) AS INTEGER)
  AND audit_events.id < sqlc.arg(before_id)
ORDER BY audit_events.id DESC
LIMIT +sqlc.arg(max_rows);

-- name: ListSensitiveKeys :many
SELECT pattern FROM sensitive_keys
WHERE service_id = +sqlc.arg(service_id)
ORDER BY pattern;

-- name: DeleteSensitiveKeys :exec
-- Clears a database's key patterns; replacing them is this plus
-- AddSensitiveKey in one transaction.
DELETE FROM sensitive_keys WHERE service_id = +sqlc.arg(service_id);

-- name: AddSensitiveKey :exec
INSERT INTO sensitive_keys (service_id, pattern) VALUES (?, ?);
