-- name: GetEmailSettings :one
SELECT * FROM email_settings WHERE id = 1;

-- SetEmailSettings writes the platform's SMTP settings and clears the
-- last outcome. A NULL password keeps the one stored; an empty one
-- removes it.
-- name: SetEmailSettings :exec
INSERT INTO email_settings (id, host, port, security, username, password, from_addr, from_name, reply_to, updated_by, updated_at)
VALUES (1, sqlc.arg(host), sqlc.arg(port), sqlc.arg(security), sqlc.arg(username), sqlc.narg(password), sqlc.arg(from_addr), sqlc.arg(from_name), sqlc.arg(reply_to), sqlc.arg(updated_by), sqlc.arg(updated_at))
ON CONFLICT (id) DO UPDATE SET
    host = excluded.host, port = excluded.port, security = excluded.security, username = excluded.username,
    password = coalesce(excluded.password, email_settings.password),
    from_addr = excluded.from_addr, from_name = excluded.from_name, reply_to = excluded.reply_to,
    updated_by = excluded.updated_by, updated_at = excluded.updated_at,
    last_ok_at = NULL, last_error = '', last_error_at = NULL;

-- name: DeleteEmailSettings :execrows
DELETE FROM email_settings WHERE id = 1;

-- name: SetEmailOK :exec
UPDATE email_settings SET last_ok_at = ?, last_error = '', last_error_at = NULL WHERE id = 1;

-- name: SetEmailError :exec
UPDATE email_settings SET last_error = ?, last_error_at = ? WHERE id = 1;

-- name: AddEmail :one
INSERT INTO email_outbox (kind, org_id, ref, to_addr, subject, nonce, body, status, next_at, error, created_at, done_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- CountQueuedEmail is how many mails wait to be sent, in all and for one
-- org (org_id).
-- name: CountQueuedEmail :one
SELECT count(*) AS total, count(CASE WHEN org_id = +sqlc.arg(org_id) THEN 1 END) AS org
FROM email_outbox WHERE status = 'queued';

-- CountEmailTo is how many mails went to an address since a time (Unix
-- seconds), for the per-address limit.
-- name: CountEmailTo :one
SELECT count(*) FROM email_outbox WHERE to_addr = +sqlc.arg(to_addr) AND created_at >= +sqlc.arg(since);

-- ListDueEmail is the queued mails due by now (Unix seconds), oldest
-- first.
-- name: ListDueEmail :many
SELECT * FROM email_outbox WHERE status = 'queued' AND next_at <= +sqlc.arg(now) ORDER BY next_at, id LIMIT +sqlc.arg(lim);

-- NextEmailAt is when the next queued mail is due (Unix seconds), 0
-- with none queued.
-- name: NextEmailAt :one
SELECT CAST(coalesce(min(next_at), 0) AS INTEGER) FROM email_outbox WHERE status = 'queued';

-- name: FinishEmail :one
UPDATE email_outbox SET status = ?, attempts = attempts + 1, error = ?, done_at = ?, body = NULL WHERE id = +? AND status = 'queued'
RETURNING *;

-- name: RetryEmail :one
UPDATE email_outbox SET attempts = attempts + 1, error = ?, next_at = ? WHERE id = +? AND status = 'queued'
RETURNING *;

-- ListEmail is the latest mails, newest first, for the console.
-- name: ListEmail :many
SELECT email_outbox.id, email_outbox.kind, email_outbox.ref, email_outbox.to_addr, email_outbox.subject, email_outbox.status,
       email_outbox.attempts, email_outbox.next_at, email_outbox.error, email_outbox.created_at, email_outbox.done_at,
       coalesce(orgs.slug, '') AS org
FROM email_outbox LEFT JOIN orgs ON orgs.id = email_outbox.org_id
ORDER BY email_outbox.id DESC LIMIT +sqlc.arg(lim);

-- EmailCounts is how many mails wait, and how many failed since a time
-- (Unix seconds).
-- name: EmailCounts :one
SELECT count(CASE WHEN status = 'queued' THEN 1 END) AS queued, count(CASE WHEN status = 'failed' THEN 1 END) AS failed
FROM email_outbox WHERE status = 'queued' OR (status = 'failed' AND done_at >= +sqlc.arg(since));

-- LatestEmailForRefs is the newest mail about each of refs: an invite's
-- row shows how its mail went.
-- name: LatestEmailForRefs :many
SELECT ref, status, error, done_at, CAST(max(id) AS INTEGER) AS id FROM email_outbox WHERE ref IN (sqlc.slice(refs)) GROUP BY ref;

-- DeleteOldEmail removes mails done before a time (Unix seconds).
-- name: DeleteOldEmail :execrows
DELETE FROM email_outbox WHERE done_at < +sqlc.arg(before);

-- RetryEmailNow makes every queued mail due at now (Unix seconds): the
-- SMTP settings changed, so retries needn't wait out their backoff.
-- name: RetryEmailNow :exec
UPDATE email_outbox SET next_at = ? WHERE status = 'queued' AND next_at > +sqlc.arg(now);
