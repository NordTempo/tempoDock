# AGENTS.md

Tempo Dock: a self-hosted, Railway-style deploy platform. One Go binary,
`dock`, is the CLI, the daemon (`dock serve`: Huma API with SSE, SQLite,
embedded React UI) and the node runtime (Docker, BuildKit, Caddy). Start
with [README](README.md) and [architecture](docs/architecture.md).

## Performance comes first

The daemon shares a small server with the apps it hosts. Read
[docs/performance.md](docs/performance.md) before changing the daemon, a
query or the UI. The rules that matter most:

- **Idle means idle.** No polling, tickers or timers without a doc comment
  justifying them and an entry in performance.md's table. Use Docker events,
  the event bus and SSE.
- **SQLite:** no N+1 (a fixed number of queries per request), an index for
  every filter (check `EXPLAIN QUERY PLAN`), and write parameters as
  `col = +?` / `LIMIT +?`: plain `col = ?` makes modernc's SQLite re-prepare
  the statement on every call.
- **Stream, never buffer** uploads, logs and images. No unbounded caches.
- **SSE:** encode an event once, batch writes, never block a publisher.
- **UI:** route-split, no heavy dependencies, no repainting animations, cap
  long lists, patch the query cache from events instead of refetching.
- **CLI:** nothing runs at start-up that `--help` doesn't need.
- **`make perf` must pass.** Add a benchmark (`*_bench_test.go`, `b.Loop`,
  `b.ReportAllocs`) for new hot paths. Update `scripts/perf/baseline.json`
  with `make perf-baseline` only for intended changes, and say why.

## Commands

```sh
make dev                # daemon on the in-memory fake runtime (no Docker), 127.0.0.1:7070
make test               # Go unit tests (fake runtime, temp SQLite)
make test-integration   # against real Docker, Caddy and BuildKit (-tags integration)
make lint               # golangci-lint (gofmt, goimports, errorlint, ...)
go vet ./...
make generate           # sqlc, OpenAPI spec (web/openapi.json) and the UI client
make web && make build  # UI into web/dist, then bin/dock with it embedded
make perf               # performance budgets (docs/performance.md)

cd web
pnpm dev                # UI with hot reload, proxying /api to the daemon
pnpm check              # typecheck, Biome, and a stale API client check
pnpm format             # Biome fixes
```

Go version: `go.mod`. Node 24, pnpm. Run `make lint`, `go vet ./...`,
`make test` and (for UI changes) `pnpm check` before committing.

## Layout

```
cmd/dock/              entry point
cmd/tempobase/         tempoBase's entry point (ADR 0024)
internal/apitypes/     the API's JSON contract; the CLI imports it, no server deps
internal/api/          Huma handlers, auth, SSE streams, loopback endpoints
internal/mcp/          the MCP endpoint for agents (ADR 0017): tools over core
internal/core/         domain logic: projects, services, variables, releases,
                       deploy state machine, domains, permissions, audit, event bus
internal/store/        SQLite: migrations/*.sql, queries/*.sql, sqlc output (*.sql.go)
internal/node/         runtime interface; docker/, build/, caddy/, remote/, fake/
internal/cli/          Cobra commands, human and --json output
internal/datapanel/    the Data panel's rules, shared by core and tempoBase
internal/data/         database engines for the panel (data/pg)
internal/tempobase/    tempoBase's server: App, tbhttp, tbstore, tbmcp
internal/httpkit/      HTTP plumbing shared by both servers; mcpkit the same for MCP
internal/registry/     the platform image registry next to the control plane
internal/update*/      finding and applying Dock releases (ADR 0028)
internal/backup/       restic jobs for stateful services; cpbackup the control plane's
internal/secrets/      AES-GCM for values at rest
internal/pack/         source archive for `dock deploy`
internal/repoconfig/   dock.toml parsing
web/                   React + Vite + Tailwind + TanStack Router/Query; web/tempobase
scripts/perf/          the `make perf` budget checker and baseline
docs/                  design docs; decisions/ holds the ADRs
e2e/                   scenarios against a live server (see e2e/run.sh)
```

Some directories have their own AGENTS.md with the rules for working
there: `internal/core`, `internal/store`, `internal/node`, `internal/cli`,
`internal/tempobase` and `web`. Read the one for the directory you change.

- `core` depends on `store` and the `node.Runtime` interface, never on
  Docker or HTTP. Every core operation takes the calling `Actor` and checks
  permissions itself.
- Changing a query or migration: edit `internal/store/queries` or
  `migrations`, run `go generate ./internal/store`, commit the output.
- Changing the API: types in `internal/apitypes`, handlers in
  `internal/api`, then `make generate` so `web/openapi.json` and
  `web/src/api/schema.d.ts` stay in sync (CI checks).

## Go conventions

- `gofmt` + `goimports` with the module as local prefix (see
  `.golangci.yml`). Wrap errors with `%w`; match with `errors.Is` /
  `errors.AsType`.
- A short doc comment above every function and type that says what it is
  for and how it's used. Keep comments in sync with the code.
- Tests: unit tests for `core` use `internal/node/fake` and a temp SQLite
  (see the `harness` in `internal/core/core_test.go`); integration tests are
  behind `//go:build integration`. Tests are focused: no smoke tests or
  "regression tests" for deleted code.
- Errors a user sees say what to do next. Same words everywhere: project,
  environment, service, release, variable, domain, volume.

## TypeScript conventions

- Strict TypeScript, `noUncheckedIndexedAccess`; no `any` (Biome errors on
  it). Prefer inferred types; derive from the generated API schema
  (`web/src/api/schema.d.ts`) instead of redeclaring shapes.
- File-based routes in `web/src/routes` (TanStack Router, auto code
  splitting); data access in `web/src/data` (queries, mutations, live
  events); features in `web/src/features`.
- UI: follow `docs/design/README.md`, the T3 style: style A for the app,
  style G for the data panel and tempoBase, with the mockups in
  `docs/design/t3-style/`. Dark near-black backgrounds, information
  dense, no decorative chrome, no em dashes in copy. Screens not yet
  migrated still use the old look; migrate a screen as a whole.

## Commits and PRs

- Conventional commits with a scope: `feat(cli): ...`, `fix(core): ...`,
  `perf(api): ...`, `docs: ...`, `test(core): ...`, `chore: ...`. Subject in
  the imperative or as a plain statement of the result, lower case.
- The body says what was wrong and how it was fixed, with numbers for
  performance changes.
- One logical change per commit. Don't commit `bin/`, `web/dist` (except
  `.gitkeep`) or `.data/`.
