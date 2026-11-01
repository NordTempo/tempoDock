# Performance

"Lightweight" and "Fast" are product pillars: the daemon shares a small
server with the apps it hosts, so every megabyte and every wake-up it costs
is taken from them. These rules apply to every change, by people and agents
alike. `make perf` checks the budgets; CI runs it on every pull request.

## Budgets

Checked by `make perf` ([scripts/perf](../scripts/perf)). Product targets are
in [experience](experience.md#performance-targets); the budgets keep
headroom below them. Dock and tempoBase are measured with `GOMAXPROCS=4`,
a small server, so a runner's core count doesn't move their memory.

| What | Budget | Measured (8-core Ryzen VM) |
|---|---|---|
| Daemon idle memory | ≤ 30 MB anonymous RSS | 11-12 MB |
| Daemon idle CPU | ≤ 2 clock ticks in 30 s | 0 |
| Daemon idle wake-ups | ≤ 0.2/s, plus 0.2/s per open SSE stream | 0.00-0.03/s; 1.0-2.1/s with 20 streams |
| Daemon memory with 20 SSE clients | ≤ 30 MB | 14-17 MB |
| CLI start-up (`dock context list`, `dock --help`) | ≤ 20 ms median (target 50) | 4.5-5 ms |
| API read p95, 8 concurrent clients, seeded (60-service project) | ≤ 20 ms | 0.4-6 ms; with a staged change, overview 6-8 ms and changes 3-4.5 ms |
| UI initial JS (entry + modulepreloads, gzip) | ≤ 200 KB | 116 KB |
| UI initial JS as the daemon serves it | ≤ 200 KB on the wire, cacheable | 113 KB, then 0 (immutable) |
| UI initial CSS (gzip) | ≤ 30 KB | 4.6 KB |
| UI initial fonts (preloaded or declared by the initial CSS, woff2 as shipped) | ≤ 100 KB | 64 KB (Inter and JetBrains Mono, Latin subsets) |
| UI fonts (all font files) | ≤ 150 KB | 64 KB |
| Binary size | ≤ baseline + 10% | 32.1 MB |
| Go benchmarks | ns/op ≤ baseline + 30%, allocs/op ≤ baseline + 15% (+2) | see `scripts/perf/baseline.json` |

Memory is anonymous RSS (`RssAnon`): what the process really costs, without
the mapped binary. The daemon runs with `GOMEMLIMIT=64MiB` as in
`deploy/systemd/dock.service`.

### tempoBase

tempoBase, the standalone Data panel server ([ADR
0024](decisions/0024-tempobase-standalone.md)), has no runtime, statistics
tick or event stream, so its budgets are tighter. Checks `tempobase-binary`,
`tempobase-daemon` and `tempobase-ui` run by default; `make perf` builds
`bin/tempobase` once `cmd/tempobase` exists. Until then (and until
`web/tempobase` exists, for the UI) they report `skipped`, not built yet.

| What | Budget | Measured |
|---|---|---|
| Binary (linux/amd64, UI embedded) | ≤ 20 MB, and baseline + 10% once recorded (`tempobase_binary_bytes`) | 19.9 MB |
| Idle memory, after start, a login and a connection list | ≤ 20 MB anonymous RSS | not built yet (target 10 MB) |
| Idle CPU | ≤ 1 clock tick in 30 s | not built yet |
| Idle wake-ups | ≤ 0.1/s | not built yet (Go's forced GC is ~0.01/s) |
| API read p95 (`/me`, `/connections`), 8 clients, 50 connections and 20 users | ≤ 10 ms | not built yet |
| Postgres connections 65 s after use (`-tempobase-pg URL`, CI's `postgres:17`) | 0 | not built yet |
| UI initial JS (entry + modulepreloads, gzip) | ≤ 120 KB | not built yet (expected ~90 KB) |
| UI initial CSS (gzip, Dock's `index.css`) | ≤ 30 KB | not built yet |
| UI initial fonts (as Dock's; style G itself uses system fonts) | ≤ 100 KB | not built yet |
| Imports (`make tempobase-deps`, CI) | no `core`, `api`, `store`, `node`, `mcp`, `cli`, Docker, BuildKit or gRPC | passes (skipped until `cmd/tempobase` exists) |

tempoBase runs with `GOMEMLIMIT=48MiB`, as its image and
`deploy/tempobase/tempobase.service` set it.

### Dock Analytics' store

`dock-analytics` ([ADR 0037](decisions/0037-analytics.md)) runs in its own
container on the analytics server, so it has budgets of its own. The
`analytics` check builds it (`make build-analytics`, CGO), seeds 1M
events (a busy site's year, plus most page loads' Web Vitals and 1% with
an error) and runs it with the container's settings.

| What | Budget | Measured |
|---|---|---|
| Browser script `/_dock/a/s.js`, gzipped | core ≤ 2 KB; errors and Web Vitals ≤ 2 KB more | 0.8 KB; 1.0 KB more |
| Idle memory, after start | ≤ 60 MB anonymous RSS | 34-38 MB |
| Idle CPU, wake-ups | ≤ 1 tick in 30 s, ≤ 0.1/s | 0, 0 |
| Dashboard p95, 1M events, every range | ≤ 100 ms | 17-42 ms |
| Filtered dashboard p95, 1M events, up to 30 days | ≤ 200 ms | 22-61 ms |
| Filtered dashboard p95, 1M events, a year | ≤ 400 ms | 53-274 ms |
| Ingest over HTTP, batches of 1,000 | ≥ 5,000 events/s | 45-62k events/s (80-97k before A2; a batch now writes up to three tables) |
| Errors and Web Vitals views p95, 1M events, every range (error groups with their series; vitals overall, by page and device, and LCP over time) | ≤ 200 ms | 13 ms (24 hours) to 84 ms (a year) |
| Peak memory through all of it | ≤ 512 MB (the container's limit) | 366-397 MB (234-259 MB before A2, mostly the ingest's vitals) |

DuckDB holds memory outside its `memory_limit` (freed operator memory,
exact distinct counts), so the store runs one query at a time with a
192 MB limit, counts filtered visitors with HyperLogLog, and gives memory
back after a query that leaves it above 192 MB and a minute after its
last request (`Store.Trim`).

## Running it

```sh
make perf                        # builds the UI and binary, checks everything (~2-3 min)
make perf PERF_FLAGS='-checks bench'          # only some checks: binary,cli,daemon,ui,bench,analytics,tempobase-binary,tempobase-daemon,tempobase-ui
make perf PERF_FLAGS='-tempobase-pg postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable'  # also tempoBase's Postgres idle close
make perf PERF_FLAGS='-idle 5s'               # shorter idle windows while iterating
make perf-baseline               # re-record binary size and benchmarks
go test -run '^$' -bench . -benchmem ./internal/core   # one package's benchmarks
```

- **Benchmarks** live in `*_bench_test.go` files; `make perf` finds them by
  that name. They cover the hot paths: project overview, service and release
  lists, audit log, variable resolution, event bus fan-out, SSE writes,
  Caddy config rendering, source packing, `dock.toml` parsing, prepared
  SQLite queries, the retention pass, the alert engine's event handling,
  the access check for login-protected services, traffic counting, and
the analytics ingest and spool (`BenchmarkIngestEvent`,
`BenchmarkAppend`: 4 µs and 6 allocations an event, budget 20 µs;
`BenchmarkIngestError` and `BenchmarkIngestVitals`, 5 µs) and the
forwarder's error counting for alerts (`BenchmarkAnalyticsWritten`). Use
  `b.Loop()` and `b.ReportAllocs()`, seed realistic data (the core
  benchmarks use a 60-service project), and add one for every new hot
  path.
- **Comparing across machines.** A fixed pure-Go workload is timed next to
  the benchmarks; ns/op is compared relative to it, so a slower CI runner
  doesn't read as a regression. The fastest of 5 runs counts (noise only
  adds time), and a package over budget is run once more before it fails.
  Benchmarks named `parallel=` depend on core count and are gated on
  allocations only. API latency budgets scale with the same factor on
  machines slower than the baseline's.
- **Updating the baseline.** When a change makes something slower on
  purpose, or a new benchmark is added, run `make perf-baseline` on a quiet
  machine and commit `scripts/perf/baseline.json` with the change, saying why
  in the commit message. Never update it to silence a regression you didn't
  mean.
- **Waivers.** `baseline.json` lists checks known to be over budget, with
  the reason and the fix. They are reported as `waived` instead of failing.
  Add one only for a pre-existing problem that has an owner, and remove it
  with the fix (`make perf` says when a waived check passes).

There are no waivers at the moment.

## Idle means idle

An idle daemon does nothing: 0 CPU and almost no wake-ups. Live data is
event driven: Docker's event stream, the in-process event bus and SSE.

- The statistics tick (below) is the one always-on periodic job; alerts
  and retention ride on it (ADR 0020). `make perf` measures idle windows
  between two ticks and budgets the tick with `BenchmarkStatsTick50`.
- No polling, tickers or timers without a written justification in a doc
  comment, and an entry in the table below with its interval and cost.
- Periodic work runs only while it has a reason to (a deploy in progress, a
  domain waiting for DNS, a client connected), and stops by itself.
- Prefer a timer to the next deadline over a fixed ticker; prefer backoff
  over a fixed retry interval.
- Resource stats and logs are read only while someone is viewing them.
- Traffic counting ([ADR 0033](decisions/0033-traffic-statistics.md))
  has no timer: the counter blocks on its UDP socket until Caddy sends a
  line, and the stats tick drains it. Caddy never waits for it.
  `BenchmarkCounterAdd` is the cost per request, `BenchmarkTrafficTick50`
  the tick's traffic step.
- Release checks ([ADR 0028](decisions/0028-updates-from-the-console.md))
  run when an operator opens the console or runs `dock update`, and are
  reused for 15 minutes. Nothing checks in the background.

Everything periodic in the daemon, the CLI and tempoBase (which has nothing else: no statistics tick, no session sweeper, no changeset timer):

| What | Where | Interval | Runs only while | Cost |
|---|---|---|---|---|
| Statistics tick | `internal/core/stats.go` | 1 min, on the minute | always (ADR 0015) | a few file reads per server and container, plus one `memory.stat` per systemd unit and container for the memory breakdown (ADR 0032, `BenchmarkHostStats`), one transaction, one `usage` event per server and per environment (`BenchmarkStatsTick50`) |
| Traffic on the tick | `internal/core/traffic.go` | with the statistics tick | always (ADR 0033) | one drain per server; with requests, one transaction and one `traffic` event per environment (`BenchmarkTrafficTick50`, ~7 ms for 50 busy services); at each new hour a trim and prune |
| Alerts on the tick | `internal/core/alerts.go` | with the statistics tick | always (ADR 0020) | in memory: disk use and offline time per server, quiet crash loops; one reminder query only while alerts are open (part of `BenchmarkStatsTick50`) |
| Retention pass | `internal/core/retention.go` | at most hourly, on the statistics tick | always (ADR 0020) | three indexed reads when nothing is due (`BenchmarkRetentionPass`, ~50 µs); otherwise batches of 200 deletes, 10 per kind |
| Disk prune | `internal/core/retention.go` | at most hourly per server, on the statistics tick | a server's Docker disk is past `prune_disk_percent` | one image list and removals, a BuildKit prune on builders, in the background |
| Alert delivery retry | `internal/core/alerts_notify.go` | 0, 10 s, 1 min, 5 min, 15 min | a channel has notifications queued | one HTTP POST or SMTP session per attempt |
| Live statistics | `internal/core/stats.go` | 3 s | someone watches a server or service live | the same reads for what is watched |
| SSE heartbeat | `internal/api/streams.go` | 25 s | an SSE stream is open | one small write per stream |
| Domain DNS/TLS checks | `internal/core/domains.go` | 5 s, backing off to 5 min | a domain is pending | a DNS lookup and TLS dial per domain |
| Cloudflare queue | `internal/core/cloudflare_queue.go` | once, at the earliest retry (5 s, backing off to 5 min per row); after a 429, at its `Retry-After` (5 min without one) | a tunnel or a domain through one is pending a retry (ADR 0036); otherwise it sleeps until kicked | one to five Cloudflare API calls per row, one at a time |
| Registry restart wait | `internal/registry/platform.go` | every 100 ms, up to 1 min | the registry restarted for a storage change or migration (ADR 0034) | one `GET /v2/` per step until it answers, about a second |
| Health check | `internal/node/docker/health.go` | at once, then 50, 100, 200, 400 ms, then 1 s | a release is health checking | one inspect + HTTP/TCP probe |
| Cutover drain | `internal/core/deploy.go` | once, 12 s after a cutover | a release was replaced | one timer |
| Docker event stream reconnect | `internal/node/docker/events.go` | backoff | the stream failed | one reconnect |
| BuildKit readiness | `internal/node/build/buildkitd.go` | 250 ms | BuildKit is starting for a build | one gRPC call |
| Node event stream retry | `internal/node/remote/client.go` | 1 s | a node's event stream failed | one reconnect |
| Node reconnect | `internal/node/remote/node.go` | jittered backoff | a node lost its control plane | one dial |
| Registry tunnel wait | `internal/node/remote/registry.go` | once, up to 10 s | a registry connection on a node waits for a free tunnel | one timer per waiting connection |
| Backup scheduler | `internal/core/backups_schedule.go` | once, at the next due backup (daily per stateful service, and daily for the control plane itself, ADR 0021) | always: the control plane's own daily backup keeps it armed | one goroutine and one timer; a pass is four queries, then one job container per due backup. The control plane's backup is a `VACUUM INTO` of the database streamed with the keys and build logs into one restic job |
| Update pickup | `internal/core/update.go` | once, 30 s after asking | the control plane asked its updater for an update (ADR 0028) | one timer and one stat; the updater normally stops the daemon first |
| Updater health check | `internal/updater/system.go` | 1 s, up to 90 s | the root updater applies an update or rolls one back (its own process, not the daemon) | one HTTP GET to the local API, or one small file read on a node |
| CLI update wait | `internal/cli/update.go` | 2 s | `dock update apply` waits for an update | one GET |
| CLI backup wait | `internal/cli/backup.go` | 2 s | `dock backup run`/`restore` (or `run --control-plane`) waits for a run | one GET |
| Data connection idle close | `internal/data/pg` | once, 60 s after last use | a database connection is open (Dock and tempoBase) | one timer per connection |
| tempoBase audit prune | `internal/tempobase` | at most hourly, inline when an event is written; no timer | something is being audited | one indexed delete of events past `audit_retention_days`, at most 1000 |
| Git push retry | `internal/core/git.go` | once, 10 s | a queued push waits for a busy upload slot (ADR 0018) | one timer, then one worker step |
| CLI deploy follow refetch | `internal/cli/follow.go` | 15 s | `dock deploy` is following releases | one GET per open release |
| CLI domain wait | `internal/cli/domains.go` | 10 s | `dock domain add` waits for DNS | one GET |
| Analytics spool tail | `internal/node/collect` | once, a second after the first event that arrives while the tail is caught up | events wait to go to the control plane (ADR 0037) | one timer; then one spool read of up to 1,000 events |
| Analytics forwarder retry | `internal/core/analytics.go` | 1 s, doubling to 30 s | the store doesn't answer or a write fails | one cursor request to the store |
| Analytics alerts | `internal/core/analytics_alerts.go` | with the statistics tick; the traffic check once an hour | analytics is on | in memory: spikes that calmed down, new-error alerts a day old; once an hour one store query for every site's page views in an hour and the 7 days before. Errors are counted per forwarded batch (`BenchmarkAnalyticsWritten`, 52 µs for 1,000 events with 10 errors; a batch without errors is one `bytes.Contains`) |
| Analytics salts | `internal/core/analytics.go` | with the statistics tick | always; does nothing on the same UTC day | at midnight: two inserts, a delete, one call per server; then the store's daily retention (one delete per site) and location database check (a download on a month's first day) |
| Analytics store start | `internal/core/analytics_store.go` | 250 ms, doubling to 5 s, for at most 2 minutes | a start or move of the store waits for it to answer | one health request to the store |
| Analytics live view | `internal/core/analytics_live.go` | 30 s, or 2 s while the site's events arrive | someone watches a site live (ADR 0037) | one live query to the analytics store, one SSE write per viewer when the view changed |
| Analytics store trim | `cmd/dock-analytics/serve.go` | once, a minute after the last request | the store was asked something (ADR 0037) | a checkpoint and two `SET memory_limit`, then `debug.FreeOSMemory` |
| Analytics rollups | `cmd/dock-analytics/store/rollup.go` | no timer: woken by a write that brings events for a finished day, or the first write of a new day | there are days to roll up | per site and day, one transaction of about 25 grouped inserts (0.27 s for a day of a 100M-event-a-year site) |

## SQLite

- **No N+1.** A request runs a fixed number of queries however many rows it
  shows: load per project or environment, assemble in Go
  (`internal/core/overview.go` is the pattern). A query inside a loop over
  services, releases or events is a bug.
- **An index for every filter and order.** Run `EXPLAIN QUERY PLAN` on every
  new or changed query against a seeded database. No `SCAN` of a table that
  grows (releases, events, audit, variables, domains) and no `USE TEMP
  B-TREE FOR ORDER BY` on one. Paginate with `id < ?`, never `OFFSET`.
- **Prepared statements.** sqlc queries are prepared once per connection
  (`store.Prepare`), and the pool keeps all connections open. Don't build
  SQL strings at runtime.
- **The re-prepare pitfall.** The SQLite in `modernc.org/sqlite` is built
  with `SQLITE_ENABLE_STAT4`, so the planner looks at bound values for
  `column = ?` on an indexed column, for `LIMIT ?` and for `LIKE ?`, and
  then re-prepares (re-parses and re-plans) the statement on every
  execution. That costs about as much as not preparing at all, and the
  parser's allocations go through modernc's libc `malloc`, which has one
  global lock: concurrent requests serialize on it
  (`BenchmarkPreparedQuery`: 31 µs/op with 8 clients for `= ?`, 8 µs/op for
  `= +?`). Write `column = +?` (the index is still used) and `LIMIT +?`, or
  `CAST(? AS INTEGER)` if sqlc can't infer a type through the unary plus;
  match a prefix with a range (`action >= ? AND action < ?`) instead of
  `LIKE ?`. Range comparisons on the rowid (`id < ?`) are fine. Check with a
  CPU profile: `_sqlite3Reprepare` must not show up.
- **Short transactions.** Writes take the lock up front (`_txlock=immediate`);
  never hold a transaction across a runtime call, a build or network I/O.

## Live events and SSE

- **Encode once.** An event is JSON-encoded once however many clients get
  it (`core.LiveEvent.JSON`). Never marshal per subscriber.
- **Batch.** Send what is queued behind an event in one write with one
  flush (`subscription.drain`, `sseWriter.write`).
- **Bounded and non-blocking.** Publishing never blocks: a slow subscriber
  misses events and gets `lagged`, then refetches. Every SSE write has a
  deadline, so a client that stops reading can't hold a stream.
- **Patch, don't refetch.** Clients apply events to what they have; only
  `lagged` or an event they can't apply triggers a refetch, and then of one
  query, not everything.

## Memory

- **Stream, never buffer** uploads, build contexts, images, build logs and
  app logs: `io.Copy` to disk or the client. Cap what must be read
  (`io.LimitReader`), like `dock.toml`.
- **No unbounded caches or maps.** Anything kept in memory has a size bound
  or is keyed by something finite and removed when it goes away (a
  subscription, a deploy). App logs are read from Docker, never stored.
- Project rename canonicalization keeps one current name per renamed project ID
  for the daemon's lifetime. It is bounded by projects, not rename count, and
  avoids DB reads for in-flight events. There are no historical aliases or timers
  (`BenchmarkBusRenamedProject`).
- **Mind allocations on hot paths.** Reuse buffers per stream, preallocate
  slices with known length, avoid `fmt.Sprintf` in loops. Benchmarks report
  allocs/op; the baseline gates them.
- **GOMEMLIMIT** is set by the systemd unit; don't fight it with manual
  `runtime.GC()` calls.

## CLI

- No work in `init()`: the CLI and daemon are one binary, so start-up cost
  is paid by every command. Package-level variables are initialized on
  every start too; keep them cheap (a small regexp is fine, reading files or
  building large tables is not).
- Don't load config, open the database or dial the network for commands
  that don't need it (`--help`, `context list`, `version`).
- Commands print as they go; follow streams instead of polling.
- The client-only build (`make build-cli`, tag `dockcli`, 8.5 MB against
  34 MB) must not pull in the daemon: code that needs `core`, `api`,
  `store` or `node/*` goes in `internal/cli` files tagged `//go:build
  !dockcli` (see `server_cmds.go`).

## UI

- **Route splitting.** Every route is its own chunk (TanStack Router
  `autoCodeSplitting`); the entry holds only the shell. Check the "UI
  initial JS" row after adding a dependency or a shared import.
- **No heavy dependencies.** No date, chart, editor or icon libraries for
  something a few lines or the platform can do. A new runtime dependency
  needs a reason in the PR.
- **No continuously repainting animations** (spinners, shimmer, pulse,
  blur): they keep the GPU busy on high-refresh displays. Static state
  indicators instead.
- **Cap or virtualize long lists** (releases, audit, logs, build output):
  render a window or a page, not thousands of rows.
- **SSE patches, not refetch storms.** Live events patch the query cache
  (`web/src/data/live.ts`); never invalidate broad query keys per event, and
  never poll.
- **Fonts** are woff2 and subset, self-hosted, `font-display: swap`, and
  loaded by the shell CSS, not by route chunks
  ([design](design/README.md#fonts-and-performance)). The "UI initial
  fonts" row counts every font the initial CSS declares or `index.html`
  preloads.
- **Serve assets compressed and cached** (`internal/api/ui.go`). Text
  files go out gzip-compressed (each compressed once, on its first
  request, and kept), hashed files under `/assets/` with `Cache-Control:
  public, max-age=31536000, immutable`, and `index.html` with `no-cache`
  and an ETag. The "UI initial JS as served" row of `make perf` checks it.

## Reviewer checklist

- [ ] `make perf` passes (CI `perf` job), or the PR explains a baseline
      update.
- [ ] New periodic work has a doc comment, stops when idle, and is in the
      table above.
- [ ] New queries: no N+1, an index for the filter, `EXPLAIN QUERY PLAN`
      checked, `= +?` / `LIMIT +?` for parameters.
- [ ] Nothing buffers an upload, log or image in memory; new caches are
      bounded.
- [ ] Live data comes from events; nothing polls.
- [ ] A new hot path has a benchmark in a `*_bench_test.go` file.
- [ ] UI: new dependencies justified, initial JS within budget, long lists
      capped, no repainting animations.
- [ ] CLI: nothing new runs at start-up.
