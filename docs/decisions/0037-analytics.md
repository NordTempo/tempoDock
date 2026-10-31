# 0037: Dock Analytics

Status: accepted (the plan agreed with Saile; the budgets revised by the
agent with the spike's numbers below, and the UI mocks picked by the
agent on Saile's direction).

## Context

Saile runs Rybbit for web analytics. Dock already fronts every domain
with Caddy and runs every app, so it can collect page views itself:
first party, without cookies, and with the service and release that
served each page. The agreed plan (Postplan `1p14abrxa1lp`) puts Rybbit's
core into Dock, plus session replay, error tracking, Web Vitals and
public dashboards, in phases A0 to A4.

| Force | Means here |
|---|---|
| Cookieless | No cookies, localStorage or sessionStorage; no banner needed. Nothing that names a visitor is stored |
| Idle means idle | A site without traffic costs nothing; no polling, timers only while there is work ([performance](../performance.md)) |
| The dock binary | Stays pure Go: no CGO, no size change. `go build ./cmd/dock` must never link the engine |
| Small servers | One 4 GB VPS runs everything out of the box. Optionally the store lives on another server (the homelab) that may be off for hours |
| Nothing lost | Production keeps collecting while the analytics server is down |

## Decision

| Topic | Decision |
|---|---|
| Sites | A site is a set of hostnames in a project. Every event records its site, hostname, service and release. History follows a hostname when its domain moves to another service; the move offers "start a new site" instead. Staging hostnames get their own sites |
| Engine | DuckDB 1.5.6 (`duckdb-go/v2 v2.10506.0`) in `dock-analytics`, its own Go module (`cmd/dock-analytics/go.mod`), so the main module's `go.mod` never mentions it. Run by Dock as an infrastructure container on the analytics server, like BuildKit; started the first time analytics is turned on |
| Store API | HTTP on loopback with a per-install token (`internal/analytics`: `Store`, `Client`, wire types). Write a batch, read a source's cursor, overview, series, breakdown, health |
| Writes | One writer, the control plane's forwarder, in batches (up to 1,000 events or 1 s). A batch carries its spool position, saved in the same transaction as its rows: a batch sent again is skipped, so delivery is exactly once |
| Sessions | Assigned by the store as it writes: the same visitor with no gap over 30 minutes. Each event is marked with its session, the session's source (referrer, channel, UTM), whether it is the session's first or second page view and how much it lengthened it, so headline numbers are one scan |
| Rollups | Per site and UTC day: every dimension's values with visitors and counts, and the day's totals. A day is rolled up once it is over; events that arrive for it later mark it stale in the same transaction, and it is rolled up again. Unfiltered queries add up whole days and read raw events at the edges; filtered queries read raw events. Rollups are kept when retention deletes events |
| Visitors | `hash(daily salt, site, IP, user agent)`, the salt changing at midnight UTC. A day's visitors are nobody else's, so visitors and sessions add up across days exactly. Filtered queries count visitors with HyperLogLog (about 2% off): an exact distinct count of millions of visitors needs memory DuckDB's limit doesn't cover |
| Memory | Container limit 512 MB (setting). DuckDB `memory_limit` 192 MB, 2 threads, one query at a time, 30 s per query. The store gives memory back after a query that leaves it above 192 MB, and once a minute after its last request (a one-shot timer armed by requests) |
| Ingest | In every daemon, on its own loopback listener. Caddy routes `/_dock/a/*` on analytics-enabled hostnames there, setting the client address and a `site.service.release` tag from the routing table, so the ingest keeps no state. The script is `/_dock/a/s.js` (core under 2 KB gzipped), events go to `/_dock/a/e` |
| Spool | Every event goes to the local spool first, also on the analytics server: append-only segment files, 16 MB each, capped (default 256 MB). At the cap the oldest replay data goes first, then the oldest events, counted |
| Forwarding | The control plane keeps a tail stream open to each server's spool over the node link while the analytics server is online, writes to the store, and acknowledges by position; acknowledged segments are deleted. Woken by appends, never polling. Live visitors are counted from what it forwards, published only while someone watches |
| Salts | Made by the control plane, today's and tomorrow's handed to every server over the link; older ones deleted |
| IP addresses | Cut to /24 (IPv6 /48) at ingest, for the location lookup only (DB-IP City Lite, CC BY 4.0, on the analytics server); never stored |

### Changed by the spike

| Plan | Now | Why |
|---|---|---|
| Rollups "kept" after retention; dashboards on raw events | Daily rollups for every unfiltered query | Raw events alone took 1.2 s for a year at 10M events and ran out of memory at 100M |
| Container 512 MB, `memory_limit` below it | `memory_limit` 192 MB, one query at a time, trims | With `memory_limit` 256 to 384 MB the process peaked at 777 to 1,054 MB: DuckDB holds freed operator memory and exact distinct hash tables outside its limit |
| Exact visitors everywhere | HyperLogLog for filtered queries | Cut the peak of filtered year queries at 10M from about 750 to about 500 MB |
| Any filter under 500 ms at 10M | At 10M, under 500 ms up to 30 days; a filtered full year takes 0.7 to 1.5 s | Filtered queries read raw events; see Consequences |

### UI

From the A0 mocks (Postplan `sa1qgjmy39ux`), the variants with the best
use:

| Screen | Pick |
|---|---|
| Service Analytics tab | C: the dashboard with a live column (live visitors, the live feed, the chart's markers), plus A's Events and "Served by" panels under the grid |
| Site settings | C: sections, each setting a "default (value) / custom" switch, the CLI equivalent under the form |
| Replay player | A: player and scrubber, the session's pages, events and errors in a column that follows playback |

## The spike

`dock-analytics seed` writes synthetic traffic through the store's real
write path: a year of sessions with a weekly rhythm, 2,000 pages,
300 referrers, 120 countries with 40 cities each, a browser and device
mix, UTM campaigns on 8% of sessions, custom events and outbound clicks,
5 sites (90% to one), 2 services (the domain moves on day 200) and a
release every 3 days. `dock-analytics bench` loads the main site's
dashboard: the overview and the one before it, the series and nine
breakdowns (pages, entry pages, referrers, channels, countries,
browsers, OS, devices, events), four at a time, unfiltered and with each
of eight filters. Numbers are through the HTTP API with the shipped
settings, on an 8-core VM (Ryzen 9 7900, 32 GB). The dock-lab VM was
unreachable ("No route to host"); its numbers follow with A1.

### Data

| Events | Seed rate (write path, 10k batches) | Database | Bytes/event | Rollup backfill (a year) |
|---|---|---|---|---|
| 1M | 208k/s | 57 MB | 57 | 8 s |
| 10M | 168k/s | 487 MB | 49 | 22 s |
| 100M | 141k/s | 4.37 GB | 44 | 98 s |

### Dashboards (p95 of the whole dashboard)

| Events | Range | Unfiltered | Filtered: best to worst of 8 |
|---|---|---|---|
| 1M | 24 h | 17 ms | 13 to 23 ms |
| 1M | 30 days | 18 ms | 22 to 60 ms |
| 1M | a year | 41 ms | 53 to 274 ms |
| 10M | 24 h | 34 ms | 16 to 35 ms |
| 10M | 30 days | 20 ms | 57 to 338 ms |
| 10M | a year | 89 ms | 731 to 1,502 ms |
| 100M | 24 h | 27 ms | 30 to 105 ms |
| 100M | 30 days | 25 ms | 632 to 2,671 ms |
| 100M | a year | 174 ms | 5.9 to 16.1 s |

The slowest filters are the ones that don't keep whole sessions (a
service, a page substring): their bounce rate and session length group
the matching events by session.

### Budgets

| Budget (plan) | Result |
|---|---|
| 1M: dashboard p95 under 100 ms | met at every range (41 ms for a year) |
| 1M: any filter under 200 ms | met up to 30 days (60 ms); a year 53 to 274 ms, one filter over |
| 10M: dashboard p95 under 200 ms | met at every range (89 ms for a year) |
| 10M: any filter under 500 ms | met up to 30 days (338 ms); a year 0.7 to 1.5 s |
| 100M: measured, with disk size | above; 4.37 GB |
| Ingest 5,000 events/s into the container | 92k to 97k events/s over HTTP in 1,000-event batches (write p95 11 ms); 5,000/s sustained has write p95 14 ms. Homelab: with A1 |
| Idle memory under 60 MB | 38 MB anonymous RSS after start; 0 CPU ticks and 0 wake-ups in 30 s idle |
| Peak memory under the container's 512 MB | 234 MB at 1M, 451 MB at 10M through every filtered year; 598 MB at 100M, so a site that size needs the limit raised |

### SQLite, for the record

The same rows (with the session marks) copied into SQLite (modernc, as
Dock links it) with an index on `(site_id, ts)`, the same dashboard
without rollups, 1M events. The middle column is DuckDB in-process
before rollups existed (384 MB, four queries at once); the last is the
shipped store over HTTP, which trims its memory after big queries and so
reads some of them cold:

| | SQLite | DuckDB, raw events only | DuckDB with rollups |
|---|---|---|---|
| 30 days, unfiltered | 403 ms | 19 ms | 18 ms |
| 30 days, filtered | 171 to 359 ms | 11 to 27 ms | 22 to 60 ms |
| A year, unfiltered | 3,895 ms | 152 ms | 41 ms |
| A year, filtered | 1,292 to 3,617 ms | 27 to 92 ms | 53 to 274 ms |
| File | 175 MB | 46 MB | 57 MB |

## Why this shape

**DuckDB in its own module and container.** Columns compress to 44 to
57 bytes per event with every dimension, and group-bys over a month of a
busy site take milliseconds where SQLite takes hundreds. The price is
CGO and a 69 MB binary, which a separate module keeps out of `dock`
entirely: `go build ./cmd/dock` can't link what its `go.mod` doesn't
name.

**UTC days.** The daily salt makes a day the unit of a visitor, so
rollups are exact sums. Charts in a site's own time zone would need
hourly rollups with visitor sketches; days are UTC until someone needs
otherwise.

**One query at a time.** DuckDB runs each query on all its threads, so
running two at once barely helps throughput but doubles the peak. With
one, a busy year's filtered dashboard stays under the container's limit.

**Spool everything.** Writing straight to the store on the analytics
server would be a second path for the one case that needs it least. With
every event in a spool first, the store has one writer, batching falls
out of the forwarder, and the store being down (an upgrade, a restart)
loses nothing.

## Consequences

- `dock` stays pure Go and its size and memory budgets are untouched;
  the analytics container costs 38 MB idle and up to about 450 MB while a
  filtered year of a 10M-event site is queried.
- Filtered queries over months of a very busy site take seconds (100M
  events a year: 0.6 to 2.7 s for 30 days, 6 to 16 s for the year), and
  its store needs more than 512 MB (598 MB peak). Such sites are beyond what
  Dock targets; a sessions table or rollups per filter would be the next
  step if one appears.
- Filtered visitor counts are estimates (about 2%); unfiltered ones are
  exact.
- Dashboard days are UTC days.
- `make perf` gains the store's budgets: idle memory, ingest rate and
  query p95 on 1M seeded events (`dock-analytics seed`, `bench`).
