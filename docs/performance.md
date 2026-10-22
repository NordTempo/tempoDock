# Performance

"Lightweight" and "Fast" are product pillars: the daemon shares a small
server with the apps it hosts, so every megabyte and every wake-up it costs
is taken from them. These rules apply to every change, by people and agents
alike. `make perf` checks the budgets; CI runs it on every pull request.

## Budgets

Checked by `make perf` ([scripts/perf](../scripts/perf)). Product targets are
in [experience](experience.md#performance-targets); the budgets keep
headroom below them.

| What | Budget | Measured (8-core Ryzen VM) |
|---|---|---|
| Daemon idle memory | ≤ 30 MB anonymous RSS | 11-12 MB |
| Daemon idle CPU | ≤ 2 clock ticks in 30 s | 0 |
| Daemon idle wake-ups | ≤ 0.2/s, plus 0.2/s per open SSE stream | 0.00-0.03/s; 1.0-2.1/s with 20 streams |
| Daemon memory with 20 SSE clients | ≤ 30 MB | 14-17 MB |
| CLI start-up (`dock context list`, `dock --help`) | ≤ 20 ms median (target 50) | 4.5-5 ms |
| API read p95, 8 concurrent clients, seeded (60-service project) | ≤ 20 ms | 0.3-12 ms; with a staged change, overview and changes 18-31 ms (waived, see below) |
| UI initial JS (entry + modulepreloads, gzip) | ≤ 200 KB | 116 KB |
| UI initial CSS (gzip) | ≤ 30 KB | 4.6 KB |
| UI fonts (all font files) | ≤ 150 KB | 0 |
| Binary size | ≤ baseline + 10% | 32.1 MB |
| Go benchmarks | ns/op ≤ baseline + 30%, allocs/op ≤ baseline + 15% (+2) | see `scripts/perf/baseline.json` |

Memory is anonymous RSS (`RssAnon`): what the process really costs, without
the mapped binary. The daemon runs with `GOMEMLIMIT=64MiB` as in
`deploy/systemd/dock.service`.

## Running it

```sh
make perf                        # builds the UI and binary, checks everything (~2-3 min)
make perf PERF_FLAGS='-checks bench'          # only some checks: binary,cli,daemon,ui,bench
make perf PERF_FLAGS='-idle 5s'               # shorter idle windows while iterating
make perf-baseline               # re-record binary size and benchmarks
go test -run '^$' -bench . -benchmem ./internal/core   # one package's benchmarks
```

- **Benchmarks** live in `*_bench_test.go` files; `make perf` finds them by
  that name. They cover the hot paths: project overview, service and release
  lists, audit log, variable resolution, event bus fan-out, SSE writes,
  Caddy config rendering, source packing, `dock.toml` parsing, and prepared
  SQLite queries. Use `b.Loop()` and `b.ReportAllocs()`, seed realistic data
  (the core benchmarks use a 60-service project), and add one for every new
  hot path.
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

Current waivers: the project overview and pending-changes diff with a
staged change resolve every service's variables with one domain query per
service, on top of the re-prepare pitfall below.

## Idle means idle

An idle daemon does nothing: 0 CPU and almost no wake-ups. Live data is
event driven: Docker's event stream, the in-process event bus and SSE.

- The statistics tick (below) is the one always-on periodic job. `make
  perf` measures idle windows between two ticks and budgets the tick with
  `BenchmarkStatsTick50`.
- No polling, tickers or timers without a written justification in a doc
  comment, and an entry in the table below with its interval and cost.
- Periodic work runs only while it has a reason to (a deploy in progress, a
  domain waiting for DNS, a client connected), and stops by itself.
- Prefer a timer to the next deadline over a fixed ticker; prefer backoff
  over a fixed retry interval.
- Resource stats and logs are read only while someone is viewing them.

Everything periodic in the daemon and CLI:

| What | Where | Interval | Runs only while | Cost |
|---|---|---|---|---|
| Statistics tick | `internal/core/stats.go` | 1 min, on the minute | always (ADR 0015) | a few file reads per server and container, one transaction, one `usage` event per server and per environment (`BenchmarkStatsTick50`) |
| Live statistics | `internal/core/stats.go` | 3 s | someone watches a server or service live | the same reads for what is watched |
| SSE heartbeat | `internal/api/streams.go` | 25 s | an SSE stream is open | one small write per stream |
| Domain DNS/TLS checks | `internal/core/domains.go` | 5 s, backing off to 5 min | a domain is pending | a DNS lookup and TLS dial per domain |
| Health check | `internal/node/docker/health.go` | 1 s (configurable) | a release is health checking | one inspect + HTTP/TCP probe |
| Cutover drain | `internal/core/deploy.go` | once, 12 s after a cutover | a release was replaced | one timer |
| Docker event stream reconnect | `internal/node/docker/events.go` | backoff | the stream failed | one reconnect |
| BuildKit readiness | `internal/node/build/buildkitd.go` | 250 ms | BuildKit is starting for a build | one gRPC call |
| Node event stream retry | `internal/node/remote/client.go` | 1 s | a node's event stream failed | one reconnect |
| Node reconnect | `internal/node/remote/node.go` | jittered backoff | a node lost its control plane | one dial |
| Data connection idle close | `internal/data/pg` | once, 60 s after last use | a database connection is open | one timer per connection |
| CLI deploy follow refetch | `internal/cli/follow.go` | 15 s | `dock deploy` is following releases | one GET per open release |
| CLI domain wait | `internal/cli/domains.go` | 10 s | `dock domain add` waits for DNS | one GET |

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
- **Fonts** are woff2 and subset.
- **Serve assets compressed and cached.** Hashed files under `/assets/`
  should go out gzip-compressed with `Cache-Control: public,
  max-age=31536000, immutable`, and `index.html` with `no-cache`. Not done
  yet: today every page load downloads the raw JS again (the "UI initial JS
  as served" row of `make perf`).

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
