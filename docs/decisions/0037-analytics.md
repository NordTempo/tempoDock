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
| Shipping | The release carries `dock-analytics-linux-{amd64,arm64}`, built on Debian 12 on each platform's own runner and listed in `SHA256SUMS`; there is no image to publish. The control plane downloads the binary for the analytics server's arch with the updater's source and token when analytics runs (never on installs that don't use it), checks it, and sends it over the node link to a remote analytics server once per release. The container is `gcr.io/distroless/cc-debian12:nonroot` (pinned, 37 MB on disk) with the binary mounted read-only: nonroot, no capabilities, read-only root, the data volume at the user's home. The store always matches the control plane's version; a development build points `[analytics] store_binary` at `make analytics-binary`'s output |
| Moving the store | `dock analytics server <server>` moves the store with its data: the forwarders hold off (events wait in the spools), the old store streams its checkpointed database file through the control plane into an empty store on the new server, which restarts on it; once the copy's SHA-256 and event count match, the new server becomes the analytics server and the old one's container, volume and binary are removed. A failed move leaves the store where it was and removes the partial copy. An offline old server's data can't be copied: `--start-empty` starts the new store empty, and a store found on a server that isn't the analytics server (once the analytics store answers) is removed when it connects |
| Failures | Why the store didn't start, or a move failed, is in the status (`dock analytics`, the console's Analytics page). Nothing retries on a timer: the store starts on daemon start, when its server reconnects, and when the status is read while it doesn't answer |
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

### Sites in practice (A1)

| Topic | Decision |
|---|---|
| Model | `sites` (an environment, a name, settings as JSON overrides), `site_hostnames` (a hostname, its site, the service it last served) and `site_markers` (added, moved, removed). Migration 0034. A site lives in one environment, so staging and production never mix |
| Turning it on | `dock analytics enable <hostname>` (deploy on its environment): it joins `--site`, or its service's other hostnames' site, or a new site named after it. The first site starts the store |
| Moves | A hostname stays with its site when its domain is removed. Added back on another service of the environment, the site follows it and gets a "moved" marker; `domain add --analytics new` starts a new site instead, archiving the old one if it is left empty. In another environment the site comes along only if it was the hostname's alone and the actor can read it there |
| Routing | Each route carries its analytics hostnames with the tag `site.service.release`; a cutover retags with the new release before it goes active |
| Settings | Per site, each a default or an override of the install's defaults (`dock analytics defaults`, operators). A1 applies excluded paths and ignored addresses (at the ingest, from the control plane with the salts) and event retention (daily, the store keeps daily totals); replay, errors, Web Vitals and public dashboards are stored for their phases |
| Reading | One dashboard call: the overview and the period before, the series (hourly up to two days), the dashboard's breakdowns and the markers (hostname changes and the services' deploys). A store that can't be reached gives an empty dashboard that says so |
| Live | Only while someone watches. The store is the one source: a view is its last five minutes, loaded again within 2 s when the forwarders pass the site's events and every 30 s otherwise, so visitors who left drop out |
| Locations | The store downloads DB-IP's City Lite (CC BY 4.0, credited wherever locations show) into its volume at start and on a month's first day, falling back to the month before |

A1's done-when holds: `e2e/analytics-outage.sh` runs a control plane
serving a production site and a joined server running the store, each in
its own Docker-in-Docker container. With 200 page views before, during
and after the store's server is off for two minutes (a reboot), the store
ends with all 600. The 200 sent during the outage fall in the outage's
own time window. Meanwhile they wait in the control plane's spool, 84 KB.

### Errors and Web Vitals (A2)

| Topic | Decision |
|---|---|
| Script | `s.js` is served per site: the core, plus `ev.js` spliced in when the site's "errors and vitals" setting is on (the default). Core 0.8 KB gzipped, errors and vitals 1 KB more. Variants are built on first use and kept (a handful). Scripts are cached for an hour, so a changed setting reaches visitors within the hour; the ingest also drops errors and vitals of a site that turned them off |
| Errors | `error` and `unhandledrejection`, at most 10 a page load, each message once; the message (500 bytes), type and stack (4 KB) go as kind 4 |
| Groups | The ingest takes the stack's top frame that names a script (Chrome's and Firefox's/Safari's formats), keeps the function and the script's path without line, column, query or a bundler's content hash, and fingerprints type, message (digit runs made alike) and that frame with FNV-1a. So a group survives deploys of a bundled app; minified function names that change per build still split it until source maps (later) |
| Web Vitals | Measured by hand-written observers rather than the `web-vitals` library (its 2 KB alone would be the whole budget): LCP, CLS (largest burst of shifts under a second apart, within five seconds), INP (the slowest interaction, web-vitals' value up to 50 interactions), FCP and TTFB, all relative to a prerender's activation. Sent once per page load, when the page is first hidden, as kind 5. The ingest keeps 0 to 10 minutes and a CLS up to 100; anything else is unmeasured |
| Storage | Their own tables, `errors` and `vitals`, never the events table: the dashboard's counts, sessions and rollups don't see them. An error carries its visitor and its session (looked up, not lengthened). `error_groups` keeps each fingerprint's first time and first release, which outlive a query's range: a write knows whether a group is new. A page load's vitals are one row without visitor or session (random 64-bit IDs would double the table), times as integers: about 20 bytes a page load |
| Percentiles | p75 from a reservoir sample of 2,048 per group (`reservoir_quantile`, within about a percentile point): bounded memory and several times faster than a t-digest or an exact quantile. One call gives the overall row (with good and poor counts), any number of breakdowns (p75 and samples only, for the top values) and one chosen vital over time; each count or percentile costs about the same, so nothing is computed that the view doesn't show. At 1M events, the errors and vitals views for a year take 84 ms (a t-digest of every vital and dimension took 510 ms for one breakdown) |
| Groups in the store | `error_groups` is insert-only: a group's first time and first release. The store keeps the set of groups in memory (as many as the error retention keeps), so a batch of known errors costs no query; a group's latest message and last time are read from its errors |
| Cost | At 1M seeded events, ingest went from 80-97k to 45-62k events/s and the store's peak from about 250 to 370-400 MB: every page view brings a vitals row, and a batch writes up to three tables. Both are well within the budgets (5,000 events/s, the container's 512 MB) |
| Retention | Errors by the site's error retention (90 days); a group left without errors goes, and is new again if it comes back. Vitals by the event retention |
| Filters | Errors and vitals filter and break down by what they record: page, hostname, country, browser and version, OS, device, service, release |
| Reading | API, CLI (`dock analytics errors`, `dock analytics vitals`) and UI. The UI follows the mocks' pick |
| Alerts | Through the alert engine (ADR 0020) and its channels, with no timer of their own. **New error**: the store's write answers the groups it saw for the first time; each opens a warning, at most 5 open per site (a flood is a spike and a handful, not dozens), resolved after a day (no longer new). **Error spike**: the forwarder counts each site's errors per minute of their own time (a backlog from an outage isn't a spike) for the last hour, in memory; 20 or more in five minutes and five times the usual rate alerts at once, and the statistics tick resolves it when the rate is down. **Traffic drop**: once an hour, ten minutes into the next, the statistics tick asks the store for every site's page views in that hour and the same hour of the 7 days before (one query); under 30% of the median, for a site with a median of 50 or more, alerts; 60% resolves. The check skips an hour while servers came and went in the last 70 minutes or the store doesn't answer: events still on their way aren't a drop. Thresholds are constants until someone needs them configurable |

### Session replay (A3)

| Topic | Decision |
|---|---|
| Recorder | rrweb 2.2 (`@rrweb/record`, MIT), bundled from `web/src/recorder` by `pnpm build:recorder` into `internal/analytics/ingest/recorder/r.js` (76 KB, 23 KB gzipped) and served by every ingest at `/_dock/a/r.js`. `pnpm check` fails when the committed bundle is stale. `s.js` loads it only on sites with replay on, for the sampled share of page loads (`Math.random`, nothing stored), and only where the browser has `CompressionStream` |
| Masking | Every input masked, always; the site's "mask all text" masks all text; `data-dock-block` leaves an element out. Canvas, fonts and images aren't recorded |
| Chunks | Every 5 s, at 1,000 events, and when the page is hidden: the events gzipped by the browser, the rest (segment, sequence, first and last times, the browser's clock, the page) in the query. A chunk is at most 384 KB gzipped and a page load 8 MB; past either the page stops recording. 384 KB keeps a spooled chunk (base64) under the replay spool's line and a node link message |
| Segments | Each page load is a segment with a random 64-bit ID. Nothing in the browser links them: the store stitches a visitor's segments with no gap over 30 minutes into one replay when it reads them, as sessions are made from events |
| Spool | A replay spool of its own on every server, a quarter of the spool cap (64 MB by default), with lines up to 600 KB, forwarded like the events' as `replay:server:N`. At its cap its oldest chunks go. This is "replays are dropped first": a long outage fills the replay spool and costs replays, and replays can never push out page views. Node protocol 12; an older node keeps recording nothing |
| Storage | The store recompresses each chunk with zstd into one file per site and UTC day, `replay/<site>/<day>/chunks.zst`, synced before the index rows (`replay_chunks`: segment, sequence, offset, length, times in the server's clock) commit. Retention is the site's replay retention (30 days): the day's rows, then its directory. Reading a replay streams its segments' frames, decompressed, gzipped once by the store and passed through by the control plane |
| Timeline | A replay's page views, custom events and errors are its visitor's in its time span, read from the events and errors tables: no extra data in the recording |
| Access | Project read, like the dashboard. Fetching a replay's recording (`.../replays/{id}/events`, one stream of all its page loads) is audited as `analytics.replay_view`, once per view; the list and the timeline aren't, they show no page content |
| Moving and backups | A store's snapshot is a tar of its database and its replay files; a move copies both. The control plane's daily backup (ADR 0021) now includes the store's snapshot as `data/analytics/restore.tar`; a control plane restored from it fills its new, empty store from it once the store runs. Before A3 the store had no backup at all. With the store's server offline, the backup goes without it and logs why |

### Goals, funnels and agents (A4)

| Topic | Decision |
|---|---|
| Goals | A page (a path, `*` matching any run of characters) or a custom event (its name), named, at most 20 per site, in Dock's database (`site_goals`, migration 0036). Their conversions are counted by the store over the dashboard's range and filters in one scan: visitors and sessions that reached each (HyperLogLog, about 2% off) and how often; the rate is over the range's visitors |
| Funnels | Two to eight such steps (`site_funnels`, steps as JSON), at most 20 per site. A session reaches a step when it has a matching event at or after it reached the one before: one join per step in the store. Cookieless sessions end at midnight UTC and after 30 minutes away, so funnels are within a visit |
| Cost | At 1M events, five goals and a three-step funnel take 8 ms for a day and 69 ms for a year (budget 200 ms, `make perf`) |
| MCP | Read-only tools for everything the Analytics tab shows: `analytics_sites`, `analytics_stats`, `analytics_breakdown`, `analytics_errors`, `analytics_vitals`, `analytics_replays` (listed, never their recordings: watching is for the UI, which audits it), `analytics_goals`, `analytics_funnel`. Filters as the API and CLI take them (`country:is:NO`). Changing goals stays with people (UI, CLI, API) |

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

A1 builds the Analytics tab and the site settings with what A1 collects:
the panels for pages, sources, locations, devices, events and what
served the visits; clicking a value filters by it; the chart's markers
and the live column. Site settings show the settings A1 applies
(excluded paths, ignored addresses, event retention); replay, error and
public dashboard settings join with their phases. Operators get Console ›
Analytics: the store's state, the analytics server and the site
defaults. Breakdowns label services and releases ("web r3") and keep
their IDs as the values filters take; a filter may also name a service.

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
