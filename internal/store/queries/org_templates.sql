-- name: ListOrgTemplates :many
-- An organization's templates by name (ADR 0083).
SELECT * FROM org_templates WHERE org_id = +? ORDER BY name;

-- name: GetOrgTemplate :one
SELECT * FROM org_templates WHERE org_id = +? AND name = +?;

-- name: UpsertOrgTemplate :one
-- Saves a template, replacing one of the same name.
INSERT INTO org_templates (org_id, name, body, created_by, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (org_id, name) DO UPDATE SET body = excluded.body, created_by = excluded.created_by, updated_at = excluded.updated_at
RETURNING *;

-- name: DeleteOrgTemplate :execrows
DELETE FROM org_templates WHERE org_id = +? AND name = +?;

-- name: DeleteOrgTemplates :exec
-- An organization's templates, when it is deleted.
DELETE FROM org_templates WHERE org_id = +?;
