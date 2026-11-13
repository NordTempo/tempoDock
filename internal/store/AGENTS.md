# internal/store

Dock's SQLite state: `migrations/*.sql` (schema), `queries/*.sql` (sqlc
input) and the generated `*.sql.go`. `store.go` opens the database and
migrates it; `stmts.go` prepares each query on first use and reuses it.

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
- A CHECK that lists values from `apitypes` (database engines) lists all
  of them, and `database_setting_test.go` checks it against the list
  (`apitypes.DataEngines`). A new value widens every such CHECK.
- Every column a query filters or sorts on is covered by an index.
  Partial indexes (`WHERE revoked_at IS NULL`) for "open" subsets.
- A new table holding a project's, environment's, service's, release's
  or site's ID gets a statement in `projectDeletes`
  (`project_delete.go`), so deleting an archived project removes its
  rows (ADR 0077). `TestProjectDeletesCoverForeignKeys` catches a
  missing one with a foreign key; one without needs you to remember.

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

Tests open a migrated database with `storetest.Open(t, path)`: it
migrates once per test binary and copies the result, which is far faster
than `store.Open` on an empty file (seconds under `-race`).

`internal/tempobase/tbstore` is tempoBase's store and follows the same
rules (`go generate ./internal/tempobase/tbstore`).
