-- name: ListWildcardCertificates :many
-- Every base domain's certificate row (bounded by base domains), for the
-- wildcard worker and the Address page.
SELECT * FROM wildcard_certificates ORDER BY base_domain;

-- name: SaveWildcardCertificate :exec
-- A successful issuance: the chain and sealed key, and no error.
INSERT INTO wildcard_certificates (base_domain, chain, key, not_before, not_after, error, attempts, retry_at, updated_at)
VALUES (?, ?, ?, ?, ?, '', 0, NULL, ?)
ON CONFLICT (base_domain) DO UPDATE SET
    chain = excluded.chain, key = excluded.key, not_before = excluded.not_before, not_after = excluded.not_after,
    error = '', attempts = 0, retry_at = NULL, updated_at = excluded.updated_at;

-- name: FailWildcardCertificate :exec
-- A failed attempt: its error, the failures so far and the next try (NULL
-- waits for a change), keeping a certificate issued before.
INSERT INTO wildcard_certificates (base_domain, error, attempts, retry_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (base_domain) DO UPDATE SET
    error = excluded.error, attempts = excluded.attempts, retry_at = excluded.retry_at, updated_at = excluded.updated_at;

-- name: DeleteWildcardCertificate :exec
-- A base domain no direct server uses any more.
DELETE FROM wildcard_certificates WHERE base_domain = +?;

-- name: GetACMEAccount :one
SELECT key FROM acme_accounts WHERE directory = +?;

-- name: CreateACMEAccount :exec
-- Another process may have made it first; its key is kept.
INSERT INTO acme_accounts (directory, key, created_at) VALUES (?, ?, ?)
ON CONFLICT (directory) DO NOTHING;

-- name: RetryFailedWildcards :execrows
-- `dock cloudflare retry`: failing base domains try again now.
UPDATE wildcard_certificates SET retry_at = ? WHERE error != '';
