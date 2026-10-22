-- Saved connections and the data access policy.

-- name: CreateConnection :one
INSERT INTO connections (name, engine, host, port, database, username, tls_mode, ca_pem, url_secret, production, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetConnectionByName :one
SELECT * FROM connections WHERE name = +?;

-- name: ListConnections :many
SELECT * FROM connections ORDER BY name;

-- name: UpdateConnection :one
UPDATE connections SET
    name = ?, host = ?, port = ?, database = ?, username = ?, tls_mode = ?, ca_pem = ?, url_secret = ?, production = ?
WHERE id = +?
RETURNING *;

-- name: DeleteConnection :exec
-- Cascades to everything tempoBase keeps about it but its audit events.
DELETE FROM connections WHERE id = +?;

-- name: CountPendingChangesets :many
-- Open changesets per connection, for admins' connection list.
SELECT connection_id, count(*) AS pending FROM changesets
WHERE status = 'pending' AND expires_at > +?
GROUP BY connection_id;

-- name: ListPolicyRules :many
-- The rules set at exactly one scope: connection_id 0 for the default.
SELECT * FROM data_policy_rules
WHERE coalesce(connection_id, 0) = +CAST(sqlc.arg(connection_id) AS INTEGER)
ORDER BY subject, kind;

-- name: ListEffectivePolicyRules :many
-- The default's rules and one connection's: what can apply there.
SELECT * FROM data_policy_rules
WHERE coalesce(connection_id, 0) IN (0, +CAST(sqlc.arg(connection_id) AS INTEGER));

-- name: ListAllPolicyRules :many
-- Every rule, to decide access on every connection at once (the
-- connection list). At most six per scope.
SELECT * FROM data_policy_rules;

-- name: DeletePolicyRules :exec
DELETE FROM data_policy_rules
WHERE coalesce(connection_id, 0) = +CAST(sqlc.arg(connection_id) AS INTEGER);

-- name: AddPolicyRule :exec
INSERT INTO data_policy_rules (connection_id, subject, kind, level, sensitive)
VALUES (?, ?, ?, ?, ?);
