-- name: ListPlatformDataPolicy :many
-- The platform's data access default and ceiling (a few rows): read on
-- start and after each change, then kept in memory.
SELECT * FROM platform_data_policy ORDER BY rule, subject, kind;

-- name: DeletePlatformDataPolicy :exec
DELETE FROM platform_data_policy;

-- name: AddPlatformDataPolicyRule :exec
INSERT INTO platform_data_policy (rule, subject, kind, level, sensitive) VALUES (?, ?, ?, ?, ?);

-- name: GetOrgSiteDefaults :one
SELECT * FROM org_site_defaults WHERE org_id = +?;

-- name: SetOrgSiteDefaults :exec
INSERT INTO org_site_defaults (org_id, settings, updated_by, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (org_id) DO UPDATE SET settings = excluded.settings, updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- name: DeleteOrgSiteDefaults :exec
DELETE FROM org_site_defaults WHERE org_id = +?;
