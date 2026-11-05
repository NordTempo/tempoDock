-- name: CreateAccessRequest :one
INSERT INTO access_requests (nonce, org_id, project_id, environment_id, status, via, operation, title, action, args,
    fingerprint, reason, token_id, requested_by, audit_mark, created_at, expires_at, upload)
VALUES (?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- GetPendingAccessRequest is the pending request with a fingerprint, so an
-- identical call joins it instead of filing another.
-- name: GetPendingAccessRequest :one
SELECT * FROM access_requests WHERE fingerprint = +? AND status = 'pending';

-- name: GetAccessRequest :one
SELECT * FROM access_requests WHERE id = +?;

-- SetAccessRequestReason gives a pending request the reason a later
-- identical call sent, when it had none.
-- name: SetAccessRequestReason :exec
UPDATE access_requests SET reason = ? WHERE id = +? AND reason = '';

-- ListOrgAccessRequests is an org's requests, newest first (the inbox).
-- name: ListOrgAccessRequests :many
SELECT * FROM access_requests WHERE org_id = +? ORDER BY id DESC LIMIT +?;

-- ListTokenAccessRequests is the requests a token filed, newest first.
-- name: ListTokenAccessRequests :many
SELECT * FROM access_requests WHERE token_id = +? ORDER BY id DESC LIMIT +?;

-- DecideAccessRequest moves a pending request to approved or rejected;
-- it changes nothing (no row) if it was already decided or has expired.
-- name: DecideAccessRequest :one
UPDATE access_requests SET status = ?, decided_by = ?, decided_at = ?, comment = ?
WHERE id = +? AND status = 'pending' AND expires_at > +?
RETURNING *;

-- FinishAccessRequest records an approved request's run.
-- name: FinishAccessRequest :one
UPDATE access_requests SET status = ?, result = ?, error = ?, finished_at = ?
WHERE id = +? AND status = 'approved'
RETURNING *;

-- ExpireAccessRequests marks pending requests past their time expired.
-- name: ExpireAccessRequests :many
UPDATE access_requests SET status = 'expired', finished_at = expires_at
WHERE status = 'pending' AND expires_at <= +?
RETURNING *;

-- FailRunningAccessRequests fails requests whose run a restart cut off.
-- name: FailRunningAccessRequests :many
UPDATE access_requests SET status = 'failed', error = ?, finished_at = ?
WHERE status = 'approved'
RETURNING *;

-- CountPendingAccessRequests counts an org's pending requests, for the
-- nav's badge.
-- name: CountPendingAccessRequests :one
SELECT count(*) FROM access_requests WHERE org_id = +? AND status = 'pending' AND expires_at > +?;

-- LatestEnvironmentAudit is the newest audit event's ID in an
-- environment (0 for none): where an access request starts counting
-- changes.
-- name: LatestEnvironmentAudit :one
SELECT CAST(coalesce(max(id), 0) AS INTEGER) FROM audit_events WHERE environment_id = +?;

-- CountEnvironmentAuditSince counts the environment's changes after an
-- audit event, leaving out access requests' own (asked, decided).
-- name: CountEnvironmentAuditSince :one
SELECT count(*) FROM audit_events WHERE environment_id = +? AND id > +? AND action NOT LIKE 'request.%';

-- name: SetOrgRequestTTL :exec
UPDATE orgs SET request_ttl_hours = ? WHERE id = +?;

-- PruneAccessRequests deletes decided requests asked before a time, with
-- the audit log's retention.
-- name: PruneAccessRequests :execrows
DELETE FROM access_requests WHERE created_at < +? AND status NOT IN ('pending', 'approved');

-- ReplaceAccessRequest gives a pending request a newer call of the same
-- kind (a newer push or upload replaces the one waiting).
-- name: ReplaceAccessRequest :one
UPDATE access_requests SET nonce = ?, args = ?, upload = ?, reason = ?, requested_by = ?, created_at = ?, expires_at = ?
WHERE id = +? AND status = 'pending'
RETURNING *;

-- ListHeldUploads is the archives of requests that may still run.
-- name: ListHeldUploads :many
SELECT upload FROM access_requests WHERE upload != '' AND status IN ('pending', 'approved');

-- EndOrphanedAccessRequests rejects, for the system, pending requests
-- that can no longer run: their token was revoked or their project
-- archived. The revoke and archive paths call it.
-- name: EndOrphanedAccessRequests :many
UPDATE access_requests SET status = 'rejected', decided_by = 'system', decided_at = sqlc.arg(now), finished_at = sqlc.arg(now),
    comment = CASE WHEN project_id IN (SELECT id FROM projects WHERE archived_at IS NOT NULL)
        THEN 'its project was archived' ELSE 'the token that asked was revoked' END
WHERE status = 'pending' AND (
    token_id IN (SELECT id FROM tokens WHERE revoked_at IS NOT NULL)
    OR project_id IN (SELECT id FROM projects WHERE archived_at IS NOT NULL))
RETURNING *;
