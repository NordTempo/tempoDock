-- name: ListExpiredReleaseHistory :many
-- Failed and superseded releases whose history retention may delete:
-- older than cutoff and out of their service's rollback window (the
-- newest keep releases, by number, as ListKeptDigests). Read through
-- releases_history, so trimmed releases are never looked at again.
SELECT releases.id FROM releases
WHERE releases.history_trimmed = 0
  AND releases.status IN ('failed', 'superseded')
  AND releases.created_at < +sqlc.arg(cutoff)
  AND releases.number <= (SELECT max(newest.number) FROM releases newest WHERE newest.service_id = releases.service_id)
                         - CAST(sqlc.arg(keep) AS INTEGER)
ORDER BY releases.id
LIMIT +sqlc.arg(max_rows);

-- name: DeleteReleaseEvents :exec
DELETE FROM release_events WHERE release_id = +?;

-- name: MarkReleaseHistoryTrimmed :exec
UPDATE releases SET history_trimmed = 1 WHERE id = +?;

-- name: ListOldestAuditEvents :many
-- The start of the audit log, for retention to trim from.
SELECT id, created_at, detail_id FROM audit_events ORDER BY id LIMIT +?;

-- name: MaxAuditEventID :one
SELECT CAST(coalesce(max(id), 0) AS INTEGER) AS id FROM audit_events;

-- name: DeleteAuditEventsThrough :exec
DELETE FROM audit_events WHERE id <= ?;

-- name: DeleteAuditDetailsThrough :exec
-- Details up to an ID that no remaining event points at (details are
-- written just before their event, so a trimmed event's is below it).
DELETE FROM audit_details WHERE audit_details.id <= sqlc.arg(through)
  AND audit_details.id NOT IN (SELECT kept.detail_id FROM audit_events kept WHERE kept.detail_id IS NOT NULL AND kept.detail_id <= +sqlc.arg(through));

-- name: ListServerReleaseImages :many
-- The images of every release on one server, with what decides whether
-- a rollback may still need them: the service archived or not, the
-- release's number and status, and its registry digest. Image pruning
-- (core imagePrune) sorts them into kept, older rollbacks and unneeded.
-- In no particular order.
SELECT releases.service_id, CAST(services.archived_at IS NOT NULL AS INTEGER) AS archived,
       releases.number, releases.status, releases.image_id, releases.image_digest
FROM environments
JOIN services ON services.environment_id = environments.id
JOIN releases ON releases.service_id = services.id
WHERE environments.server_id = +sqlc.arg(server_id) AND releases.image_id != '';
