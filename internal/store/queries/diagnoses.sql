-- Deploy diagnoses (ADR 0071).

-- name: AddDiagnosis :one
INSERT INTO diagnoses (service_id, release_id, kind, rule, summary, bundle, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id;

-- name: TrimDiagnoses :exec
-- Deletes a service's diagnoses older than its newest `keep`.
DELETE FROM diagnoses
WHERE diagnoses.service_id = +sqlc.arg(service_id) AND diagnoses.id < (
    SELECT d.id FROM diagnoses d WHERE d.service_id = +sqlc.arg(service_id)
    ORDER BY d.id DESC LIMIT 1 OFFSET CAST(sqlc.arg(keep) AS INTEGER) - 1
);

-- name: GetDiagnosis :one
-- A diagnosis of a service, with its release's number.
SELECT d.id, d.release_id, d.kind, d.bundle, d.created_at, r.number AS release_number
FROM diagnoses d JOIN releases r ON r.id = d.release_id
WHERE d.id = +sqlc.arg(id) AND d.service_id = +sqlc.arg(service_id);

-- name: ListDiagnoses :many
-- A service's diagnoses newest first, without their bundles.
SELECT d.id, d.kind, d.rule, d.summary, d.created_at, r.number AS release_number
FROM diagnoses d JOIN releases r ON r.id = d.release_id
WHERE d.service_id = +sqlc.arg(service_id)
ORDER BY d.id DESC LIMIT +sqlc.arg(max_rows);

-- name: GetReleaseDiagnosisRef :one
-- The newest diagnosis of a release, without its bundle.
SELECT id, kind, rule, summary FROM diagnoses
WHERE release_id = +? ORDER BY id DESC LIMIT 1;

-- name: ListServiceDiagnosisRefs :many
-- Every diagnosis of a service without bundles, for a release list (a
-- service keeps 20 at most).
SELECT id, release_id, kind, rule, summary FROM diagnoses
WHERE service_id = +? ORDER BY id;

-- name: PruneDiagnoses :execrows
-- Deletes diagnoses captured before the cutoff (the retention pass).
DELETE FROM diagnoses WHERE created_at < +?;
