-- name: AddEnsureRunning :exec
INSERT INTO ensure_running (service_id) VALUES (?) ON CONFLICT DO NOTHING;

-- name: ListEnsureRunning :many
-- The services on a server whose active release must be started again.
SELECT r.service_id FROM ensure_running r
JOIN services s ON s.id = r.service_id
JOIN environments e ON e.id = s.environment_id
WHERE e.server_id = +sqlc.arg(server_id);

-- name: DeleteEnsureRunning :exec
DELETE FROM ensure_running WHERE service_id = +?;

-- name: ListRecoveryServices :many
-- A server's live stateful services (an applied volume, not archived, in
-- a live project, not a preview's), each with its newest succeeded backup
-- in one repository that retention hasn't removed, if it has one: the
-- recovery plan (ADR 0098). Base backups for point-in-time restore are
-- left out; a recovery loads the dump.
SELECT services.id, services.name, services.template, environments.name AS environment,
       projects.name AS project, orgs.slug AS org,
       backups.id AS backup_id, backups.kind AS backup_kind, backups.snapshot_id,
       backups.size_bytes, backups.finished_at,
       CAST(EXISTS (SELECT 1 FROM backups any_repo WHERE any_repo.service_id = services.id
              AND any_repo.operation = 'backup' AND any_repo.status = 'succeeded') AS BOOLEAN) AS ever_backed_up
FROM services
JOIN environments ON environments.id = services.environment_id
JOIN projects ON projects.id = environments.project_id
JOIN orgs ON orgs.id = projects.org_id
LEFT JOIN backups ON backups.id = (
    SELECT max(b.id) FROM backups b
    WHERE b.service_id = services.id AND b.operation = 'backup' AND b.status = 'succeeded'
      AND b.pruned = 0 AND b.kind != 'pitr' AND b.repository = +sqlc.arg(repository))
WHERE environments.server_id = +sqlc.arg(server_id)
  AND services.archived_at IS NULL AND projects.archived_at IS NULL
  AND EXISTS (SELECT 1 FROM volumes WHERE volumes.service_id = services.id AND volumes.pending = 0)
  AND NOT EXISTS (SELECT 1 FROM previews WHERE previews.environment_id = services.environment_id)
ORDER BY orgs.slug, projects.name, environments.name, services.name;
