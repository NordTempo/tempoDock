# 0003: SQLite for platform state

Status: accepted

## Context

Platform state is small: projects, services, variables, releases, tokens, audit
events. It's written by one process on one server.

## Decision

- SQLite in WAL mode, a single file in the data directory.
- `modernc.org/sqlite` (pure Go, no CGO) to keep static builds simple.
- `sqlc` for typed queries, plain SQL migrations embedded in the binary and run
  on start-up.

## Consequences

- No database service to run, tune or back up separately; backups are a file
  copy (or `VACUUM INTO`).
- Typed queries without an ORM.
- If a hosted control plane ever needs many writers or many tenants, that
  deployment may need Postgres. The `store` package is the boundary where that
  would change; we don't abstract for it now.
