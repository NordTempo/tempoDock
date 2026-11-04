-- Second factors, recovery codes and the sessions they prove (ADR 0051).

-- name: ListUserFactors :many
SELECT * FROM user_factors WHERE user_id = +? ORDER BY id;

-- name: CountUserFactors :one
SELECT count(*) FROM user_factors WHERE user_id = +?;

-- GetPasskeyByCredentialID is a passkey and its user, for passwordless
-- sign-in, which learns the user from the credential.
-- name: GetPasskeyByCredentialID :one
SELECT sqlc.embed(user_factors), sqlc.embed(users)
FROM user_factors JOIN users ON users.id = user_factors.user_id
WHERE user_factors.credential_id = +?;

-- name: CreateUserFactor :one
INSERT INTO user_factors (user_id, kind, name, credential_id, secret, created_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: DeleteUserFactor :one
DELETE FROM user_factors WHERE id = +? AND user_id = +? RETURNING *;

-- name: DeleteUserFactors :execrows
DELETE FROM user_factors WHERE user_id = +?;

-- UseTOTPStep accepts an authenticator app's code for time step step,
-- once: a step at or before the last accepted one changes nothing.
-- name: UseTOTPStep :execrows
UPDATE user_factors SET last_step = +sqlc.arg(step), last_used_at = +sqlc.arg(now)
WHERE id = +sqlc.arg(id) AND last_step < +sqlc.arg(step);

-- UsePasskey stores a passkey's credential after a sign-in, with its new
-- sign count, if it still is the one read (old): two sign-ins with the
-- same count can't both pass.
-- name: UsePasskey :execrows
UPDATE user_factors SET secret = +sqlc.arg(secret), last_used_at = +sqlc.arg(now)
WHERE id = +sqlc.arg(id) AND secret = +sqlc.arg(old);

-- name: CreateRecoveryCode :exec
INSERT INTO recovery_codes (user_id, hash) VALUES (?, ?);

-- name: DeleteRecoveryCodes :exec
DELETE FROM recovery_codes WHERE user_id = +?;

-- UseRecoveryCode spends a recovery code; 0 rows when it is wrong or
-- already spent.
-- name: UseRecoveryCode :execrows
UPDATE recovery_codes SET used_at = +sqlc.arg(now)
WHERE user_id = +sqlc.arg(user_id) AND hash = +sqlc.arg(hash) AND used_at IS NULL;

-- name: CountRecoveryCodes :one
SELECT count(*) FROM recovery_codes WHERE user_id = +? AND used_at IS NULL;

-- FailSecondFactor counts a wrong second factor on a pending session and
-- returns how many there were.
-- name: FailSecondFactor :one
UPDATE sessions SET attempts = attempts + 1 WHERE id = +? RETURNING attempts;

-- name: DeleteSessionByID :exec
DELETE FROM sessions WHERE id = +?;

-- SetSessionFactor records a second factor on a session, as a step-up.
-- name: SetSessionFactor :exec
UPDATE sessions SET factor = ?, verified_at = ? WHERE id = +?;

-- name: VerifySession :exec
UPDATE sessions SET verified_at = ? WHERE id = +?;

-- name: SetOrgRequireTwoFactor :one
UPDATE orgs SET require_two_factor = ? WHERE id = +? RETURNING *;

-- name: GetOperatorsTwoFactor :one
-- Whether operators need a second factor; no row until first set.
SELECT operators_two_factor FROM platform_settings WHERE id = 1;

-- name: SetOperatorsTwoFactor :exec
INSERT INTO platform_settings (id, operators_two_factor, updated_by, updated_at) VALUES (1, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    operators_two_factor = excluded.operators_two_factor, updated_by = excluded.updated_by, updated_at = excluded.updated_at;
