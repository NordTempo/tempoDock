# Performance

"Lightweight" and "Fast" are product pillars: the daemon shares a small
server with the apps it hosts, so every megabyte and every wake-up it costs
is taken from them. These rules apply to every change, by people and agents
alike. `make perf` checks the budgets. CI runs the same checks on every
pull request as two jobs (`make perf-artifacts` and `make perf-bench`,
with `-soft-timing`) and posts the results as a comment on it (the
`perf-comment` job), updated on each push. On shared runners timing
budgets only warn, while sizes, memory and allocations still fail, so a
**warn** row in that comment is a regression to fix, or to explain in the
PR, before merging. Timing is enforced by running `make perf` locally.
A pull request that raises a budget (a limit here or in scripts/perf,
or a baseline re-recorded higher, which the comment shows as a warning
against main's) names it in its description with the reason.

## Budgets

Checked by `make perf` ([scripts/perf](../scripts/perf)). Product targets are
in [experience](experience.md#performance-targets); the budgets keep
headroom below them, except idle memory: its budget (34 MB) is measured
with Dock Analytics on and sits above the 30 MB target. Dock and tempoBase
are measured with `GOMAXPROCS=4`, a small server, so a runner's core count
doesn't move their memory. The daemon is measured with analytics turned
on.

"Measured" is CI's `perf-artifacts` job on `main` at 226b1597
(2026-11-10), a shared 2-CPU runner: timings there are slower and noisier
than on a quiet machine.

| What | Budget | Measured |
|---|---|---|
| Daemon idle memory | ≤ 34 MB anonymous RSS | 32.0 MB (2026-11-11, 31ca4da1; 31.6-32.2 MB on main's runs that day) |
| Daemon idle CPU | ≤ 2 clock ticks in 30 s | 0 |
| Daemon idle wake-ups | ≤ 0.2/s, plus 0.2/s per open SSE stream | 0.00/s; 0.60/s with 20 streams |
| Daemon memory with SSE clients | ≤ 34 MB plus 0.15 MB per stream (36.9 MB with 20) | 32.1 MB with 20 streams |
| CLI start-up (`dock context list`, `dock --help`) | ≤ 20 ms median (target 50) | 10.5 ms, 11.4 ms |
| API read p95, 8 concurrent clients, seeded (60-service project) | ≤ 20 ms | 0.35-8.2 ms (the project overview is the slowest); with a staged change, overview 8.2 ms and changes 4.5 ms |
| UI initial JS (entry + modulepreloads, gzip) | ≤ 200 KB | 168.9 KB (24 files) |
| UI initial JS as the daemon serves it | ≤ 200 KB on the wire, cacheable | 164 KB, then 0 (immutable) |
| UI initial CSS (gzip) | ≤ 30 KB | 13.9 KB |
| UI initial fonts (preloaded or declared by the initial CSS, woff2 as shipped) | ≤ 100 KB | 63.6 KB (Inter and JetBrains Mono, Latin subsets) |
| UI fonts (all font files) | ≤ 150 KB | 63.6 KB |
| Binary size (`bin/dock`, UI and dock-probe embedded) | ≤ baseline + 10% (67.8 MB) | 66.3 MB (2026-11-10, with the UI embedded gzipped): measure before adding a dependency |
| Go benchmarks | ns/op ≤ baseline + 30%, allocs/op ≤ baseline + 15% (+2) | see `scripts/perf/baseline.json` |

`make perf` also prints, without a budget, the daemon's memory after
seeding and load (63.7 MB) and the gzipped JS of every route (727 KB in
172 files).

Memory is anonymous RSS (`RssAnon`): what the process really costs, without
the mapped binary. The daemon runs with `GOMEMLIMIT=64MiB` as in
`deploy/systemd/dock.service`.

The idle number moves with GC timing, by up to 3 MB between runs, so
the budget keeps 2 MB above main's typical reading; raise it (and say
why) only after main has failed it on several runs, as on 2026-11-11
when status pages, scanning and tracing had added about 2 MB. The
daemon's last collection runs during start-up, and whatever it allocates
after that (the first statistics ticks, each SSE handshake) stays in RSS
until the next one, as do stream handlers' stacks. A forced GC leaves the
live heap at about 10 MB with or without streams: nothing is kept per
stream beyond its goroutine. The live heap is mostly Huma's schemas of
the API types and compiled regexps.

### tempoBase

tempoBase, the standalone Data panel server ([ADR
0024](decisions/0024-tempobase-standalone.md)), has no runtime, statistics
tick or event stream, so its budgets are tighter. `make perf` builds
`bin/tempobase` and runs the checks `tempobase-binary`, `tempobase-daemon`
and `tempobase-ui`. Measured as above (CI, 2026-11-10).

| What | Budget | Measured |
|---|---|---|
| Binary (linux/amd64, UI embedded) | ≤ 21 MB (20 MB until database insights, ADR 0069), and baseline + 10% (`tempobase_binary_bytes`) | 20.2 MB, its UI embedded gzipped |
| Idle memory, after start, a login and a connection list | ≤ 20 MB anonymous RSS | 12.0 MB |
| Idle CPU | ≤ 1 clock tick in 30 s | 0 |
| Idle wake-ups | ≤ 0.1/s | 0.03/s (Go's forced GC is ~0.01/s) |
| API read p95 (`/me`, `/connections`), 8 clients, 50 connections and 20 users | ≤ 10 ms | 0.6 ms, 2.6 ms |
| Postgres connections 65 s after use (`-tempobase-pg URL`, CI's `postgres:17`) | 0 | 0 (1 right after use) |
| UI initial JS (entry + modulepreloads, gzip) | ≤ 120 KB | 116.7 KB (11 files) |
| UI initial CSS (gzip, Dock's `index.css`) | ≤ 30 KB | 5.4 KB |
| UI initial fonts (style G uses system fonts) | ≤ 100 KB | 0 |
| Imports (`make tempobase-deps`, CI's lint job) | no `core`, `api`, `store`, `node`, `mcp`, `cli`, Docker, BuildKit or gRPC | passes |

tempoBase runs with `GOMEMLIMIT=48MiB`, as its image and
`deploy/tempobase/tempobase.service` set it.

### Dock Analytics' store

`dock-analytics` ([ADR 0037](decisions/0037-analytics.md)) runs in its own
container on the analytics server, so it has budgets of its own. The
`analytics` check builds it (`make build-analytics`, CGO), seeds 1M
events (a busy site's year, plus most page loads' Web Vitals and 1% with
an error) and runs it with the container's settings.

Measured as above (CI, 2026-11-10); a timed row shows its slowest range.

| What | Budget | Measured |
|---|---|---|
| Browser script `/_dock/a/s.js`, gzipped | core ≤ 2 KB; errors and Web Vitals ≤ 2 KB more | 1.0 KB; 1.0 KB more |
| Session recorder `/_dock/a/r.js` (sampled page loads of sites that record), gzipped | ≤ 50 KB | 22.5 KB |
| Idle memory, after start | ≤ 60 MB anonymous RSS | 46.2 MB |
| Idle CPU, wake-ups | ≤ 1 tick in 30 s, ≤ 0.1/s | 0, 0.03/s |
| Dashboard p95, 1M events, every range | ≤ 100 ms | 42 ms (a year) |
| Filtered dashboard p95, 1M events, up to 30 days | ≤ 200 ms | 42 ms |
| Filtered dashboard p95, 1M events, a year | ≤ 400 ms | 166 ms |
| Ingest over HTTP, batches of 1,000 | ≥ 5,000 events/s | 37k events/s (a batch writes up to three tables) |
| Errors and Web Vitals views p95, 1M events, every range (error groups with their series; vitals overall, by page and device, and LCP over time) | ≤ 200 ms | 119 ms (a year) |
| Goals and funnels p95, 1M events, every range (five goals and a three-step funnel) | ≤ 200 ms | 74 ms (a year) |
| Peak memory through all of it | ≤ 512 MB (the container's limit) | 326 MB |

DuckDB holds memory outside its `memory_limit` (freed operator memory,
exact distinct counts), so the store runs one query at a time with a
192 MB limit, counts filtered visitors with HyperLogLog, and gives memory
back after a query that leaves it above 192 MB and a minute after its
last request (`Store.Trim`).

### The trace store

`dock-analytics traces` keeps spans for tracing over OTLP in a DuckDB
database of its own (`traces.duckdb`), in its own process and container
beside the analytics store, so the two never share a container's
headroom: bursts of writes grow DuckDB's memory past its
`memory_limit`, and in one process a trace backlog on top of analytics
went past the 512 MB limit on CI. It runs with `memory_limit` 128 MB, one
query at a time and `GOMEMLIMIT=48MiB`, in a 512 MB container. The
`traces` check seeds 2M spans (a week of a service at about 3 spans a
second) and runs it with those settings.

Measured on CI's run of the pull request that added it (#212,
2026-11-11), until `main` has run it.

| What | Budget | Measured |
|---|---|---|
| Idle memory, after start | ≤ 40 MB anonymous RSS | 27.1 MB (8.0 MB on an 8-core VM) |
| Idle CPU, wake-ups | ≤ 1 tick in 30 s, ≤ 0.1/s | 0, 0.00/s |
| A service's list p95, newest, slowest and filtered, up to 7 days | ≤ 60 ms | 29 ms (7 days) |
| The org's errors over 18 services p95, up to 7 days | ≤ 120 ms | 48 ms |
| Opening a trace p95, with its time or without | ≤ 30 ms | 9 ms |
| Span ingest over HTTP, batches of 1,000, the slower of two loads of 100k | ≥ 15,000 spans/s | 24.4k spans/s |
| Peak memory through all of it | ≤ 512 MB (the container's limit) | 304 to 387 MB |

Spans go in one table per UTC day (dropping a day is the retention, with
no delete scan). With 10M seeded spans over a week (32 bytes a span on
disk, seeded at 247k spans/s): a service's list takes 2 to 18 ms up to a
day and 7 to 70 ms over the week (slowest first is the slow one), the
org's errors 3 to 93 ms, opening a trace 3 ms with its time and 8 ms
without. A list is two queries: the page by start and duration alone,
then its columns within the box those span. Selecting more columns in
the first, or looking the page up by trace IDs over the whole range,
cost two to ten times as much. Writes at full speed take the store to
250 to 300 MB over 600k spans whatever its `memory_limit` (64 to 128 MB
tried), and CI's runs of the check peaked at 304 to 387 MB, hence the
container's 512: 384 MB was set from the first runs and failed on the
fourth.

## Running it

```sh
make perf                        # builds both UIs, dock, dock-analytics and tempobase, checks everything
make perf-bench                  # only the Go benchmarks; builds nothing
make perf-artifacts              # everything but the benchmarks (CI's perf-artifacts job)
make perf PERF_FLAGS='-checks daemon,ui'      # only some checks: binary,cli,daemon,ui,bench,analytics,traces,tempobase-binary,tempobase-daemon,tempobase-ui
make perf PERF_FLAGS='-skip bench'            # all but some
make perf PERF_FLAGS='-tempobase-pg postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable'  # also tempoBase's Postgres idle close
make perf PERF_FLAGS='-idle 5s'               # shorter idle windows while iterating
make perf PERF_FLAGS='-soft-timing'           # timing budgets warn instead of failing (CI)
make perf-baseline               # re-record binary sizes and benchmarks
go test -run '^$' -bench . -benchmem ./internal/core   # one package's benchmarks
```

Dock's daemon, the analytics and trace stores and tempoBase are checked side by
side: start-up, seeding and anything timed run one at a time, while idle
windows and waits overlap (an idle window counts only its own process's
CPU, wake-ups and memory). The CLI's start-up and the benchmarks run
alone.

- **Benchmarks** live in `*_bench_test.go` files; `make perf` runs every
  benchmark of each package that has one. They cover the hot paths: project overview, service and release
  lists, audit log, variable resolution, event bus fan-out, SSE writes,
  Caddy config rendering, source packing, `dock.toml` parsing, prepared
  SQLite queries, the retention pass, the alert engine's event handling,
  the access check for login-protected services, traffic counting, a
  site dashboard's deploy markers (`BenchmarkListSiteDeploys`), a status
  page served from its cache, rendered and read (`BenchmarkStatusServe`,
  `BenchmarkRenderStatusPage`, `BenchmarkStatusPage100`, ADR 0073), and
error issues (`BenchmarkAssembler` and `BenchmarkErrorFollowerLine`, a
log line without an error: 130 and 80 ns, no allocation;
`BenchmarkErrorsWrite50`, a tick's write of 100 issues; `BenchmarkListErrors`,
the errors page of 50 services and 200 issues),
the analytics ingest and spool (`BenchmarkIngestEvent`,
`BenchmarkAppend`: 4 µs and 6 allocations an event, budget 20 µs;
`BenchmarkIngestError` and `BenchmarkIngestVitals`, 5 µs;
`BenchmarkIngestReplay`, 27 µs and 1 KB allocated for a 20 KB replay
chunk, `BenchmarkIngestReplayMax` 3-10 KB for a full 384 KB one: bodies
and lines come from a pool of their own) and the
forwarder's error counting for alerts (`BenchmarkAnalyticsWritten`), and
masking secret values in logs (`BenchmarkRedactLine`, `BenchmarkRedactBuildLog`), a deploy diagnosis's rules (`BenchmarkDiagnoseRules`, ~20 µs), and image scanning's hook on every build, deploy, wake and move (`BenchmarkScanBusy`, 47 ns, no allocation), a match's write (`BenchmarkScanFindingsWrite`) and the Images page (`BenchmarkListImages`, 0.18 ms for 180 live releases; `BenchmarkListImagesIgnored`, 0.45 ms with 20 ignores in force), and the trace ingest (`BenchmarkIngestProto512`, an SDK's batch of 512 spans with their attributes: about 3 µs and 28 allocations a span, token, decoding, caps and masking included). Use
  `b.Loop()` and `b.ReportAllocs()`, seed realistic data (the core
  benchmarks use a 60-service project), and add one for every new hot
  path.
- **Comparing across machines.** A fixed pure-Go workload is timed next to
  the benchmarks; ns/op is compared relative to it, so a slower CI runner
  doesn't read as a regression. The fastest of 5 runs counts (noise only
  adds time), and a benchmark over budget is run once more (alone, not
  its whole package) before it fails.
  Benchmarks named `parallel=` depend on core count, and those named
  `io=` (a `b.Run("io=files", ...)` around the loop) on the disk and
  other jobs' I/O; both are gated on allocations only. API latency budgets scale with the same factor on
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

- The statistics tick (below) is the one always-on periodic job; alerts,
  retention and the other per-minute work ride on it (ADR 0020). The only
  other always-armed timer is the backup scheduler's, which fires once a
  day. `make perf` measures idle windows
  between two ticks and budgets the tick with `BenchmarkStatsTick50`.
- No polling, tickers or timers without a written justification in a doc
  comment, and an entry in the table below with its interval and cost.
- Periodic work runs only while it has a reason to (a deploy in progress, a
  domain waiting for DNS, a client connected), and stops by itself.
- Prefer a timer to the next deadline over a fixed ticker; prefer backoff
  over a fixed retry interval.
- Resource stats and logs are read only while someone is viewing them.
- Log followers ([ADR 0046](decisions/0046-log-history-and-drains.md),
  [ADR 0062](decisions/0062-org-alerts-and-operators.md#one-place-for-errors))
  have no timer: each running service container has one Logs stream,
  which blocks until the container writes. Every line is read for
  errors as it arrives (`BenchmarkErrorFollowerLine`: a lock and a byte
  scan, no allocation); found errors wait in a bounded sink for the
  statistics tick, which writes them. A remote server's lines cross its
  node link, one HTTP/2 stream per container, at most 150 per server
  outside environments with drains.
- Status pages ([ADR 0073](decisions/0073-public-status-pages.md)) have
  no timer: an uptime, alert or sleep event, or an incident's change,
  adds one to a generation counter, and a page (its incident pages,
  history and feed alike) is rendered only when someone views it after
  a change (at most once every 5 s) or a minute after its last render.
  A view is served from gzipped bytes in memory, without a query. An
  uptime_down or crash loop alert opening costs one indexed query for
  the components it would draft an incident on.
- Maintenance windows (ADR 0073) start and end on the statistics tick:
  with none scheduled or in progress, the step is one zero-time check.
- Traffic counting ([ADR 0033](decisions/0033-traffic-statistics.md))
  has no timer: the counter blocks on its UDP socket until Caddy sends a
  line, and the stats tick drains it. Caddy never waits for it.
  `BenchmarkCounterAdd` is the cost per request, `BenchmarkTrafficTick50`
  the tick's traffic step.
- Release checks ([ADR 0028](decisions/0028-updates-from-the-console.md))
  run when an operator opens the console or runs `dock update`, and are
  reused for 15 minutes. Nothing checks in the background.

Everything periodic or retried on a timer: in the daemon (control plane
and node), the CLI, the analytics store and the browser. tempoBase has
only the data connection idle close and its audit prune: no statistics
tick, no session sweeper, no changeset timer. One-shot timeouts that
bound a single request or call (a `context.WithTimeout`, a long poll's
end) are not listed.

| What | Where | Interval | Runs only while | Cost |
|---|---|---|---|---|
| Statistics tick | `internal/core/stats.go` | 1 min, on the minute | always (ADR 0015) | a few file reads per server and container, plus one `memory.stat` per systemd unit and container for the memory breakdown (ADR 0032, `BenchmarkHostStats`), one transaction, one `usage` event per server and per environment (`BenchmarkStatsTick50`) |
| Traffic on the tick | `internal/core/traffic.go` | with the statistics tick | always (ADR 0033) | one drain per server; with requests, one transaction and one `traffic` event per environment (`BenchmarkTrafficTick50`, ~7 ms for 50 busy services); at each new hour a trim and prune |
| Alerts on the tick | `internal/core/alerts.go` | with the statistics tick | always (ADR 0020) | in memory: disk, memory and CPU use (from the samples just taken, ADR 0062) and offline time per server, quiet crash loops; one reminder query only while alerts are open (part of `BenchmarkStatsTick50`). Connection alerts (Cloudflare, GitHub App, storage, registry) have no timer: they open and resolve on calls Dock makes anyway |
| Error issues on the tick | `internal/core/errors_ingest.go` | with the statistics tick | always (ADR 0062) | in memory: ends errors that waited 2 s for their next line (a lock per follower) and expires day-old error alerts; with errors since the last tick, one transaction of a few indexed statements per issue (`BenchmarkErrorsWrite50`, ~2.6 ms for 100 issues in 50 services) and an alert per new or regressed issue (at most 3 open per service). A minute without errors writes nothing |
| Sleep pass on the tick | `internal/core/sleep.go` | with the statistics tick | an environment may sleep or is asleep (ADR 0070); with none, one map length check | in memory: the environments of services with requests this minute start their idle time over, then one map walk (`BenchmarkSleepPass50`, ~1.2 µs, no allocation for 50). For an environment idle past its setting, in the background: two indexed reads, one `OpenRequests` (Caddy's admin API) and, if nothing is open, the sleep: a transaction, a route load and a `docker stop` per service |
| Wake hold | `internal/node/caddy/wake.go` (`WakeHandler`), on the control plane and on nodes | once per held request: 3 s for a page load, 30 s otherwise | a request reached an asleep environment's wake check (ADR 0070) | one timer and a channel wait per request, at most 256 per environment; on a node, one wake event over the link per environment at most every 2 s. The wake itself is one per environment however many wait: a `docker start` and the deploy's health check per service, a transaction and a route load |
| Maintenance windows on the tick | `internal/core/status_maintenance.go` | with the statistics tick | a status page's maintenance window is scheduled or in progress (ADR 0073); with none, one zero-time check | one time comparison with the next boundary; at a boundary a transaction per window, a reload of the windows (one query) and the alerts it held sent |
| Health restarts on the tick | `internal/core/health.go` | with the statistics tick | always (ADR 0043) | in memory: one map walk; for an unhealthy service whose backoff ran out, an inspect, two queries and a restart |
| Ongoing health probe | Docker, inside each app container (`cmd/dock-probe`) | 30 s per container (`health_check_interval_seconds`); 2 s during a start period | a service with something to probe runs (ADR 0043) | one exec of dock-probe by Docker: a connect and at most one HTTP request, ~4 MB for a few ms in the app's cgroup. Nothing in the daemon: it reads the result from Docker's event stream |
| Retention pass | `internal/core/retention.go` | at most hourly, on the statistics tick | always (ADR 0020) | three indexed reads when nothing is due, and three indexed statements on error issues (quiet ones resolve, old resolved ones and counts go; ADR 0062), one indexed delete of deploy diagnoses past `build_log_days` (ADR 0071) (`BenchmarkRetentionPass`, ~50 µs); otherwise batches of 200 deletes, 10 per kind. In the background, one service ID list and one `prune-logs` call per online server: a directory listing per service with log history (ADR 0046) |
| Disk prune | `internal/core/retention.go` | at most hourly per server, on the statistics tick | a server's Docker disk is past `prune_disk_percent` | one image list and removals, a BuildKit prune on builders, in the background |
| Alert delivery retry | `internal/core/alerts_notify.go` | 0, 10 s, 1 min, 5 min, 15 min | a channel, or a status page's update hook (ADR 0073), has something queued | one HTTP POST or SMTP session per attempt |
| Log drain batch | `internal/core/drains_deliver.go` | once, 1 s after a sender starts on a batch (sooner when 1,000 lines are queued) | a log drain has lines queued (ADR 0046); the sender exits when its queue is empty | one HTTP POST, or one syslog write, per batch of up to 1,000 lines; per line a follower pays one non-blocking enqueue per drain (`BenchmarkDrainEnqueue`) and the sender one encode (`BenchmarkDrainEncodeHTTP`, `BenchmarkDrainEncodeLoki`, `BenchmarkDrainEncodeSyslog`, per 1,000 lines). The HTTP client keeps one idle connection per drain host, closed 90 s after the last batch |
| Log drain retry | `internal/core/drains_deliver.go` | 1 s, doubling to 1 min | a log drain's endpoint fails; new lines past its queue (10,000 lines, 4 MB) are dropped and counted meanwhile | one delivery attempt of the same batch |
| Live statistics | `internal/core/stats.go` | 3 s | someone watches a server or service live | the same reads for what is watched |
| SSE heartbeat | `internal/api/streams.go` | 25 s | an SSE stream is open | one small write per stream |
| Domain DNS/TLS checks | `internal/core/domains.go` | 5 s, backing off to 5 min | a domain is pending | a DNS lookup and TLS dial per domain; for one not yet verified, a TXT lookup from the control plane instead (ADR 0042) |
| Cloudflare queue | `internal/core/cloudflare_queue.go` | once, at the earliest retry (5 s, backing off to 5 min per row); after a 429, at its `Retry-After` (5 min without one) | a tunnel or a domain through one is pending a retry (ADR 0036); otherwise it sleeps until kicked | one to five Cloudflare API calls per row, one at a time |
| Registry restart wait | `internal/registry/platform.go` | every 100 ms, up to 1 min | the registry restarted for a storage change or migration (ADR 0034) | one `GET /v2/` per step until it answers, about a second |
| Fake health pass | `internal/node/fake/fake.go` (PassHealthAfter) | once, 1 s after a container starts or restarts | `make dev` only (DOCK_FAKE_RUNTIME=1), for a container with a health check | one timer per start; then one health_status event |
| Health check | `internal/node/docker/health.go` | at once, then 50, 100, 200, 400 ms, then 1 s | a release is health checking | one inspect + HTTP/TCP probe |
| Cutover drain | `internal/core/deploy.go` | once, 12 s after a cutover | a release was replaced | one timer |
| Template database setup retry | `internal/core/templates.go` | 1 s, doubling, 5 attempts (about 15 s) | setting up a new template database's data access failed | one setup attempt |
| Failed release write retry | `internal/core/deploy.go` | 100 ms, doubling, 6 attempts (about 3 s) | writing a failed deploy's outcome to SQLite failed | one `UPDATE` per attempt |
| Docker event stream reconnect | `internal/node/docker/events.go` | 0.5 s doubling to 30 s | the stream failed | one reconnect |
| Docker disk usage retry | `internal/node/docker/disk.go` | 500 ms, at most 3 tries | a disk breakdown or prune met a container removed meanwhile | one `system df` |
| Caddy admin retry | `internal/node/caddy/caddy.go` | 100, 200, 400, 800 ms | Caddy's admin socket can't be reached (Caddy restarting) | one request |
| Registry migration upload retry | `internal/registry/migrate.go` | 1 s, then 2 s | one object of a migration to or from a bucket failed (ADR 0034) | one upload |
| BuildKit readiness | `internal/node/build/buildkitd.go` | 250 ms | BuildKit is starting for a build | one gRPC call |
| Node event stream retry | `internal/node/remote/client.go` | 1 s | a node's event stream failed | one reconnect |
| Node reconnect | `internal/node/remote/node.go` | jittered backoff, 1 s doubling to 30 s | a node lost its control plane | one dial (it rereads the control plane's address, so `dock node set-control-plane` needs no restart) |
| Node link keepalive | `internal/node/remote` (`DefaultKeepAlive`) | after 15 s of silence on the link | a node is connected | one HTTP/2 ping each way, and TCP keepalives; an unanswered ping closes the link after 15 s more |
| Registry tunnel wait | `internal/node/remote/registry.go` | once, up to 10 s | a registry connection on a node waits for a free tunnel | one timer per waiting connection |
| Uptime scheduler | `internal/core/uptime_sched.go` | once, at the next due check: each check's interval, 30 s at the least (ADR 0055) | uptime checks exist; with none the goroutine waits on its kick channel and no timer is armed | one goroutine and one timer; per probe one HTTP GET (on another server over the node link, or in process) and one upsert (`BenchmarkUptimeRecord`, ~18 µs) |
| Uptime rollups on the tick | `internal/core/uptime_sched.go` | with the statistics tick; works at most once an hour | uptime checks exist | an hour's minute rows into hour rows and two trims in one transaction (`BenchmarkUptimeRollup`, ~4 ms for 50 checks at 30 s); once a day also the day's hour rows into day rows for status pages and a trim (ADR 0073), one statement each |
| Snapshot claim wait | `internal/core/snapshots.go` | once, up to 5 min | an automatic snapshot (before a pre-deploy command or a `dock run` writing volumes, ADR 0047) waits for a backup, snapshot or restore of the service to end | one timer; the claim's release wakes it, nothing polls |
| Backup scheduler | `internal/core/backups_schedule.go` | once, at the next due backup (daily per stateful service, and daily for the control plane itself, ADR 0021); 10 min after its own query failed | always: the control plane's own daily backup keeps it armed | one goroutine and one timer; a pass is four queries, then one job container per due backup. Each pass also deletes volume snapshots past their age (ADR 0047): one indexed query, and a job per server only when one is due. The control plane's backup is a `VACUUM INTO` of the database streamed with the keys and build logs into one restic job |
| Job scheduler | `internal/core/jobs_schedule.go` | once, at the next due job of any service (ADR 0054); 1 min after its own query failed | an enabled job exists: with none there is no goroutine or timer (one pass at start, then it exits until a job is set) | one goroutine and one timer for every job; a pass is one query and a cron step per job (`BenchmarkJobPass`), then one job container per due job |
| Environment move retry | `internal/core/env_move.go` (startMove) | 5 s doubling to 5 min | only while a move's rollback or clean-up keeps failing (ADR 0053) | one goroutine per such move, ended by success, a server it waits for, or shutdown |
| Move hold waits for commands | `internal/core/env_move.go` (moveStop) | 1 s, up to 5 min | a move's hold starts while a command runs in the environment (ADR 0063) | one map read under a lock |
| Drain busy wait | `internal/core/server_drains.go` | once, 15 s | a drain's next move is kept from starting by a deploy, backup or snapshot (ADR 0063) | one move pre-flight |
| Forward claim timeout | `internal/node/remote/forward.go` | once, 10 s per accepted connection | a node forwards a moved service's port (ADR 0063) | one timer per connection until the control plane claims it, normally at once |
| Update pickup | `internal/core/update.go` | once, 30 s after asking | the control plane asked its updater for an update (ADR 0028) | one timer and one stat; the updater normally stops the daemon first |
| Updater health check | `internal/updater/system.go` | 1 s, up to 90 s | the root updater applies an update or rolls one back (its own process, not the daemon) | one HTTP GET to the local API, or one small file read on a node |
| CLI update wait | `internal/cli/update.go` | 2 s | `dock update apply` waits for an update | one GET |
| CLI backup and snapshot wait | `internal/cli/backup.go`, `snapshots.go` | 2 s | `dock backup run`/`restore` (or `run --control-plane`) or `dock volume snapshot` / `snapshot restore` waits for a run | one GET |
| CLI long polls | `internal/cli/login_browser.go`, `request.go` | one request held up to 30 s by the server, then the next; 2 s after a lost connection (login) | `dock login` waits for the browser, or `dock request wait` for a decision | one request per 30 s; the server answers at once on the event, nothing polls inside it |
| Data connection idle close | `internal/data/conncache` (every engine) | once, 60 s after last use | a database connection is open (Dock and tempoBase) | one timer per connection |
| tempoBase audit prune | `internal/tempobase` | at most hourly, inline when an event is written; no timer | something is being audited | one indexed delete of events past `audit_retention_days`, at most 1000 |
| Git push retry | `internal/core/git.go` | once, 10 s | a queued push waits for a busy upload slot (ADR 0018) | one timer, then one worker step |
| Health follow (browser) | `web/src/features/data/HealthView.tsx` | 5 s | a person turned on Follow in the Data panel's Health tab, Sessions view, and the page is visible; it stops after 10 minutes or on leaving the view (ADR 0069) | one insights request: a few catalog queries on the database as `dock_monitor`, 2 to 16 ms each; nothing in the daemon between requests |
| Log stream reconnect (browser) | `web/src/api/events.ts` | once per lost connection, backing off 1 s to 30 s, at most 6 tries in a row | a followed log or build log lost its connection | one timer, then one EventSource that replays the backlog |
| Replay player clock (browser) | `web/src/features/analytics/ReplayPlayer.tsx` | 250 ms | a session replay is playing | one state update |
| Session recorder flush (visitor's browser) | `web/src/recorder/recorder.ts` (`/_dock/a/r.js`) | 5 s | a sampled page load of a site that records is open (ADR 0037) | one POST of the recorded chunk, when there is one |
| CLI deploy follow refetch | `internal/cli/follow.go` | 15 s | `dock deploy` is following releases | one GET per open release |
| CLI domain wait | `internal/cli/domains.go` | 10 s | `dock domain add` waits for DNS | one GET |
| Analytics spool tail | `internal/node/collect` | once, a second after the first event that arrives while the tail is caught up | events, replay chunks or spans wait to go to the control plane (ADR 0037); each spool has its own tail | one timer; then one spool read of up to 1,000 events |
| Analytics forwarder retry | `internal/core/analytics.go` | 1 s, doubling to 30 s | the store doesn't answer or a write fails | one cursor request to the store |
| Spans forwarder retry | `internal/core/traces_forward.go` | 1 s, doubling to 30 s | tracing is on and the trace store doesn't answer or a write fails | one cursor request to the trace store; per batch one indexed query (the server's environments) and one write |
| Analytics alerts | `internal/core/analytics_alerts.go` | with the statistics tick; the traffic check once an hour | analytics is on | in memory: spikes that calmed down, new-error alerts a day old; once an hour one store query for every site's page views in an hour and the 7 days before. Errors are counted per forwarded batch (`BenchmarkAnalyticsWritten`, 52 µs for 1,000 events with 10 errors; a batch without errors is one `bytes.Contains`) |
| Analytics salts | `internal/core/analytics.go` | with the statistics tick | always; does nothing on the same UTC day | at midnight: two inserts, a delete, one call per server; then the store's daily retention (one delete per site) and location database check (a download on a month's first day) |
| Analytics store start | `internal/core/analytics_store.go` | 250 ms, doubling to 5 s, for at most 2 minutes | a start or move of the store waits for it to answer | one health request to the store |
| Analytics live view | `internal/core/analytics_live.go` | 30 s, or 2 s while the site's events arrive | someone watches a site live (ADR 0037) | one live query to the analytics store, one SSE write per viewer when the view changed |
| Analytics store trim | `cmd/dock-analytics/serve.go` | once, a minute after the last request | the store was asked something (ADR 0037) | a checkpoint and two `SET memory_limit`, then `debug.FreeOSMemory` |
| Scan quiet wait | `internal/core/scans.go` (`runScans`) | once, `[scan] quiet_seconds` (60) after the last build, deploy, wake or move ends; reset by the next | live images wait for a scan or scans are queued (ADR 0074); with none the scanner goroutine exits and no timer is armed | one goroutine and one timer; then one query for live releases without a scan, one for the next scan, and one job container on a builder |
| Scan database refresh | `internal/core/scans.go` (`kickScanRefresh`, from the retention pass) | daily per scanning builder, at the first quiet minute | a builder has a vulnerability database it last refreshed a day ago (ADR 0074) | no timer and no query: each scanner pass notes when the next builder is due, and the hourly retention pass starts the scanner once that time has passed; then a `grype db update` job (about 200 MB when there is a newer database) and a match from the stored SBOM for each live image matched against an older one; then one manifest request per live base image, through the builder's Docker, to see whether its tag moved (ADR 0074 phase 3) |
| Scan retry | `internal/core/scans.go` | 10 min doubling to 24 h, at most 5 attempts | a scan failed (a preempted one isn't failed) | one timer to the earliest retry |
| Scan jobs | Docker on a builder (`internal/node/docker/scan.go`) | per queued image, one at a time platform-wide | nothing has been busy for the quiet period (ADR 0074) | one container at CPU shares 2, at most 1 CPU and `[scan] memory_mb`, no network: an SBOM (alpine 2 s, a node app 3.3 s) then a match (about 10 s, under 256 MB); killed by any build, deploy, wake or move. The daemon streams the SBOM to disk and writes the findings in one transaction (`BenchmarkScanFindingsWrite`, about 9 ms for 2,000). The first scan on a builder downloads Grype's database (about 200 MB, 3.4 GB on disk), or with `[scan] offline` imports the one an operator uploaded (`dock scanning import`) as a step of its own |
| Trace store trim | `cmd/dock-analytics/serve.go` (`api.serve`), in `dock-analytics traces` | once, a minute after the last request | the trace store was asked something | a checkpoint and two `SET memory_limit`, then `debug.FreeOSMemory`, as for the analytics store |
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
- **Prepared statements.** sqlc queries are prepared on first use and
  reused (`internal/store/stmts.go`), once per connection they run on, and
  the pool keeps all connections open. Not up front: preparing every query
  at start (about 460 then, about 700 now) cost an idle daemon about 4 MB
  of SQLite memory for queries it never runs. A transaction that meets a query for the first
  time runs it unprepared and has it prepared in the background, since
  preparing needs a second connection. Don't build SQL strings at runtime.
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
  subscription, a deploy). App logs are read from Docker while their
  container exists. A removed container's log is streamed once into a
  gzip archive on its server (ADR 0046), bounded per service by `[logs]`,
  and read back line by line; a tail keeps only its N lines in memory
  (`BenchmarkArchivedLogsTail`). No timer of its own: archiving happens
  when a container is removed, pruning on the hourly retention pass.
- A container log line is cut at 64 KB on a rune boundary and the rest up
  to the next newline dropped, as the node link does for remote lines, so a
  container writing without newlines can't grow the daemon's buffer
  (`BenchmarkLineWriter`).
- The analytics ingest takes requests from anyone on the internet, so it
  is capped (`internal/analytics/ingest`): a replay chunk is checked
  (site records, not a bot, the day's salt, rules, rate limits) before
  its body is read, and at most 4 are read at once (503 past that), under
  900 KB each, so 3.5 MB in all. Per address (IPv6: per /64) and second,
  50 events and 5 replay chunks are kept; per site and second, 2 MB of
  events and 4 MB of chunks. Past them an event gets the usual 202 and a
  chunk 429. The counts are a 16 KB table and an entry per site, reset
  by the first request of a new second, without a timer
  (`BenchmarkIngestEventParallel/parallel=8`).
- Error issues ([ADR 0062](decisions/0062-org-alerts-and-operators.md#one-place-for-errors)):
  a follower keeps at most one error being assembled (50 lines, 8 KB)
  and a reference to its last line; the sink between two ticks holds at
  most 50 fingerprints per service and 2,000 in all, dropping and
  counting past them; a tick opens at most 10 issues per service, and a
  service has at most 1,000.
- The trace ingest takes OTLP/HTTP from apps on every server, so it is
  capped (`internal/traces`): a body is at most 2 MB as sent and 4 MB
  unpacked (413 past it), at most two are read and decoded at once (503
  with `Retry-After` past that, so at most about 12 MB in all), and each
  service may send 5,000 spans a second with a burst of twice that.
  Past the rate, spans are rejected with OTLP's partial success, which
  SDKs don't send again, so an app never waits and nothing queues. Each
  span is cut on the way in: a name of 256 bytes, 64 attributes in 4 KB,
  values of 1 KB (2 KB for URLs and statements, 8 KB for an exception's
  stack), 16 events in 12 KB. Kept spans go to the spans spool, capped
  on its own (`[traces] spool_max_mb`, 128), so spans never push out
  page views.
- Deploy diagnoses ([ADR 0071](decisions/0071-deploy-diagnosis.md))
  are captured only when a release fails, or once per crash loop or
  unhealthy alert (in a goroutine of its own, never the alert engine's):
  two node calls within 5 s, a 60-line container tail and a 40-line
  build tail (lines cut at 400 bytes), stored as at most 48 KB, 20 per
  service. In memory only an open alert's cause, a few strings per open
  crash loop or unhealthy alert.
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
- The client-only build (`make build-cli`, tag `dockcli`, 10 MB against
  66 MB) must not pull in the daemon: code that needs `core`, `api`,
  `store` or `node/*` goes in `internal/cli` files tagged `//go:build
  !dockcli` (see `server_cmds.go`).

## UI

- **Route splitting.** Every route is its own chunk (TanStack Router
  `autoCodeSplitting`); the entry holds only the shell. Check the "UI
  initial JS" row after adding a dependency or a shared import.
- **No heavy dependencies.** No date, chart or icon libraries for
  something a few lines or the platform can do. A new runtime dependency
  needs a reason in the PR. The few heavy ones load only on the routes
  that use them: CodeMirror and Glide Data Grid in the Data panel, rrweb's
  player on a replay.
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
- **Serve assets compressed and cached** (`internal/httpkit/ui.go`). The
  build gzips text files (`web/gzipDist.ts`) and the binary embeds only
  the `.gz`, which goes out as it is (decompressed on the fly for the rare
  client without gzip): nothing is compressed or kept at run time, and
  `bin/dock` is about 1 MB smaller. Hashed files under `/assets/` with `Cache-Control:
  public, max-age=31536000, immutable`, and `index.html` with `no-cache`
  and an ETag. The "UI initial JS as served" row of `make perf` checks it.

## Reviewer checklist

- [ ] `make perf` passes (CI's perf jobs), or the PR explains a baseline
      update. The PR's perf comment has no unexplained **warn** rows.
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
