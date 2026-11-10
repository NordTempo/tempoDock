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

## Fix what taught the mistake

When a review, a bot or a check catches a bad pattern, fix the code and
also whatever led to it, in the same PR: the doc or AGENTS.md rule that
pointed the wrong way, the code it was copied from, or the missing check
that would have caught it. If the cause isn't clear, ask the agent that
wrote it why it chose that, and fix what it names. A pattern left in the
codebase is an example the next change copies.

Two checks keep that visible: CI's perf comment on every pull request
(timing rows only warn there, so read them) and the lint suppression
budget in `scripts/ci/suppressions.sh` (`make lint`). Fix the code before
raising the budget.

## Commands

```sh
make dev                # daemon on the in-memory fake runtime (no Docker), 127.0.0.1:7070
make test               # Go unit tests (fake runtime, temp SQLite)
make test-integration   # against real Docker, Caddy and BuildKit (-tags integration)
make lint               # golangci-lint (gofmt, goimports, errorlint, ...)
go vet ./...
make generate           # sqlc, OpenAPI specs, the UI's API clients, the analytics recorder
make web && make build  # UI into web/dist, then bin/dock with it embedded
make perf               # performance budgets (docs/performance.md)
make test-analytics lint-analytics   # the analytics store's module (CGO)

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
cmd/dock-analytics/    the analytics store (ADR 0037): its own Go module with
                       CGO and DuckDB, run as a container; dock never links it
cmd/dock-probe/        health probe run inside app containers (ADR 0043)
cmd/dock-sync/         volume copier run in job containers (ADR 0063); both
                       are embedded into dock by internal/node/probe
internal/apitypes/     the API's JSON contract; the CLI imports it, no server deps
internal/api/          Huma handlers, auth, SSE streams, loopback endpoints
internal/mcp/          the MCP endpoint for agents (ADR 0017): tools over core
internal/core/         domain logic: orgs, projects, services, variables,
                       releases, deploy state machine, domains, permissions,
                       audit, event bus, and every feature built on them
internal/store/        SQLite: migrations/*.sql, queries/*.sql, sqlc output
                       (*.sql.go); storetest opens migrated test databases
internal/node/         runtime interface; docker/, build/, caddy/, remote/, fake/
internal/cli/          Cobra commands, human and --json output
internal/datapanel/    the Data panel's rules, shared by core and tempoBase
internal/data/         database engines for the panel (data/pg)
internal/tempobase/    tempoBase's server: App, tbhttp, tbstore, tbmcp
internal/httpkit/      HTTP plumbing shared by both servers; mcpkit the same for MCP
internal/analytics/    Dock Analytics' events, store interface and client;
                       standard library only (make analytics-deps)
internal/registry/     the platform image registry next to the control plane
internal/s3/           the small S3 client behind the registry and backups
internal/update/       finding, downloading and verifying Dock releases;
internal/updater/      applying one as root (`dock admin update`, ADR 0028)
internal/backup/       restic jobs for stateful services; cpbackup the control plane's
internal/volsync/      copying volumes between servers (ADR 0063)
internal/cloudflare/   the Cloudflare API calls Dock makes (ADR 0036)
internal/access/       signed grants for Dock-login access protection (ADR 0030)
internal/errparse/     finds and groups errors in log output (Errors tab)
internal/secrets/      AES-GCM for values at rest
internal/config/       the daemon's config file (`dock serve`, `dock node`)
internal/clientconfig/ the CLI's contexts and directory links
internal/pack/         source archive for `dock deploy`
internal/repoconfig/   dock.toml parsing (ADR 0007)
internal/projectfile/  project files for `dock apply` and `dock project export`
internal/compose/      Docker Compose to project file (`dock import compose`)
internal/cron/         cron expressions for scheduled jobs (ADR 0054)
internal/runframe/     stream framing for `dock run` (ADR 0048)
internal/version/      the build version (-ldflags)
web/                   React + Vite + Tailwind + TanStack Router/Query; web/tempobase
site/                  the landing page (Astro, static), deployed on Dock
deploy/                install.sh, systemd units, analytics and tempoBase packaging
dev/                   config for `make dev` (state in .data/)
scripts/               perf/ (the `make perf` checker and baseline), ci/, release/
docs/                  design docs and the user guide; decisions/ holds the ADRs
e2e/                   scenarios against a live server (see e2e/run.sh)
```

Some directories have their own AGENTS.md with the rules for working
there: `internal/core`, `internal/store`, `internal/node`, `internal/cli`,
`internal/tempobase`, `web` and `site`. Read the one for the directory you change.

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
  dense, no decorative chrome, no em dashes in copy.

## Commits and PRs

- Conventional commits with a scope: `feat(cli): ...`, `fix(core): ...`,
  `perf(api): ...`, `docs: ...`, `test(core): ...`, `chore: ...`. Subject in
  the imperative or as a plain statement of the result, lower case.
- The body says what was wrong and how it was fixed, with numbers for
  performance changes.
- One logical change per commit. Don't commit `bin/`, `web/dist` (except
  `.gitkeep`) or `.data/`.
- Changes reach `main` through a pull request, never a direct push. Agents
  may open and merge their own PRs: branch from the latest `main`, run the
  local checks above, open the PR (a real one, not a draft), wait for CI
  to pass, then squash-merge. A CI failure is fixed or shown to be a known
  flake before merging, never merged over.
- PR titles follow the commit convention. The description opens with the
  problem, then how it was solved, and ends with the model and harness
  that made the change.
