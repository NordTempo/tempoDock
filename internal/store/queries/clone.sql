-- name: InsertLiveVariable :exec
-- A variable that is live from the start: a cloned environment's
-- variables, where nothing runs yet (ADR 0022), or one a rollback puts
-- back.
INSERT INTO variables (environment_id, service_id, key, value, pending, updated_at)
VALUES (?, ?, ?, ?, '', ?);

-- name: SetServiceCopiedSettings :exec
-- The Dock-side settings a cloned service keeps from its source: its
-- template and its data panel settings.
UPDATE services SET template = ?, database = ?, database_url_variable = ? WHERE id = +?;
