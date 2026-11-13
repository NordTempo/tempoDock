-- CreatePasswordReset is a user's new reset link; their unused ones go
-- first (DeleteUnusedPasswordResets, in the same transaction).
-- name: CreatePasswordReset :exec
INSERT INTO password_resets (user_id, hash, created_at, expires_at) VALUES (?, ?, ?, ?);

-- name: DeleteUnusedPasswordResets :exec
DELETE FROM password_resets WHERE user_id = +? AND used_at IS NULL;

-- GetPasswordReset is a reset link by its code's hash, with its user.
-- name: GetPasswordReset :one
SELECT sqlc.embed(password_resets), sqlc.embed(users)
FROM password_resets JOIN users ON users.id = password_resets.user_id
WHERE password_resets.hash = +?;

-- UsePasswordReset spends a link; no row when it was spent already.
-- name: UsePasswordReset :one
UPDATE password_resets SET used_at = ? WHERE id = +? AND used_at IS NULL RETURNING id;

-- name: DeleteOldPasswordResets :execrows
DELETE FROM password_resets WHERE expires_at < +?;

-- ListRequestDeciders is who may decide an org's access requests and
-- wants mail about them: its admins (not temporary ones), enabled, with
-- email_requests on.
-- name: ListRequestDeciders :many
SELECT users.id, users.email, users.name FROM org_members JOIN users ON users.id = org_members.user_id
WHERE org_members.org_id = +? AND org_members.role = 'admin' AND org_members.expires_at IS NULL
  AND users.disabled_at IS NULL AND users.email_requests;

-- name: SetUserEmailRequests :exec
UPDATE users SET email_requests = ? WHERE id = +?;
