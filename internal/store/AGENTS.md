# internal/store

Dock's SQLite state: `migrations/*.sql` (schema), `queries/*.sql` (sqlc
input) and the generated `*.sql.go`. `store.go` opens the database,
migrates it and prepares every query once per connection.

## Migrations

- Files are `NNNN_name.sql`, applied in order above `PRAGMA user_version`,
  each in its own transaction. Add the next number; never edit or renumber
  a migration that has been on `main`: installs have already run it.
- Start with a comment saying what the migration is for (and its ADR).
- Rebuilding a table other tables reference: make `-- foreign_keys: off`
  the first line (see `0022_orgs.sql`). It runs with foreign keys off and
  commits only if `foreign_key_check` is clean.
- A migration that moves existing rows gets a test that migrates a
  database holding those rows: `openBefore` in `data_test.go`.
- Every column a query filters or sorts on is covered by an index.
  Partial indexes (`WHERE revoked_at IS NULL`) for "open" subsets.

## Queries

- `-- name: Foo :one|:many|:exec`, with a comment above it when the name
  doesn't say everything (what it returns, ordering, who uses it).
- Parameters in `WHERE` and `LIMIT` are `+?` or `+sqlc.arg(x)`, never
  plain `?`: plain parameters make modernc's SQLite re-prepare the
  statement on every call (`store_bench_test.go`, docs/performance.md).
  `INSERT ... VALUES (?, ...)` and `SET col = ?` stay plain.
- One query per request shape: join or aggregate instead of a query per
  row. Check new queries with `EXPLAIN QUERY PLAN` (no `SCAN` of a large
  table).
- Writes that read first go through `Store.Tx`; the connection takes the
  write lock up front (`_txlock=immediate`), so they never hit
  `SQLITE_BUSY` halfway.

After changing either directory: `go generate ./internal/store` and commit
the generated files. Never edit `*.sql.go`, `models.go` or `db.go` by
hand.

`internal/tempobase/tbstore` is tempoBase's store and follows the same
rules (`go generate ./internal/tempobase/tbstore`).
