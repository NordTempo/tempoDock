-- name: GetSecretsFormat :one
-- The format of the sealed values (ADR 0041): 2 once every value is
-- bound to where it is stored.
SELECT secrets_format FROM install WHERE id = 1;

-- name: SetSecretsFormat :exec
UPDATE install SET secrets_format = ? WHERE id = 1;
