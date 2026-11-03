-- name: GetSecretsFormat :one
-- The format of the sealed values (ADR 0041): 2 once every value is
-- bound to where it is stored.
SELECT secrets_format FROM install WHERE id = 1;

-- name: SetSecretsFormat :exec
UPDATE install SET secrets_format = ? WHERE id = 1;

-- name: ListCarriedVariables :many
-- The releases whose carried changes hold variable values, with their
-- environment, for resealing those values (ADR 0041). Runs once per
-- database, so it scans.
SELECT releases.id, services.environment_id, releases.carried FROM releases
JOIN services ON services.id = releases.service_id
WHERE releases.carried LIKE '%"variables"%';

-- name: SetReleaseCarried :exec
-- Replaces a release's carried changes, unless they changed meanwhile.
UPDATE releases SET carried = ? WHERE id = +sqlc.arg(id) AND carried = +sqlc.arg(old_carried);
