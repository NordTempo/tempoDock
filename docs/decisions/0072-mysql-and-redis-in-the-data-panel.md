# 0072: MySQL/MariaDB and Redis/Valkey in the Data panel

Status: accepted (Saile's mock pick B, 2026-11-10). Builds on
[0016](0016-database-panel.md), [0024](0024-tempobase-standalone.md) and
[0069](0069-database-insights.md).

## Context

The Data panel and its Health tab work for Postgres only. Apps on Dock
also run MySQL or MariaDB (often from `dock import compose`) and Redis
(Dock's own template). For those, people and agents fall back to `dock
connect` and a local client: no policy below the tunnel's top level, no
masking, no changesets, no audit of reads, no Health view. 0016 planned
both engines "behind the same API", but `data.Engine` is shaped around
tables and core holds one engine.

## Decision

### Engines and the interface

- Two engine IDs: `mysql` (MySQL 8.0+, MariaDB 10.6+) and `redis`
  (Redis 7.0+, Valkey 7.2+); MariaDB shows in the server version
  ("MariaDB 11.4.4"). Standalone
  servers only: no Redis Cluster or Sentinel.
- `data.Engine` is what every engine does (status, setup, grants,
  statements, changesets, insights, cancel). Capability interfaces add
  the rest: `data.Tables` (schema, pages, references) for Postgres and
  MySQL, `data.Keys` (keyspace, scan, key value) for Redis. The panel
  holds a registry by engine (`datapanel.Options.Engines`) and refuses
  the wrong capability with a message naming the right command. Core,
  the API and MCP stay engine-neutral apart from browsing.
- Every engine caches connections through `internal/data/conncache`: at
  most 2 per (database, role) and 8 per engine, each closed 60 s after
  its last use by its own timer, no goroutines.

### MySQL and MariaDB

- Our own client for the text protocol (handshake with optional TLS,
  `caching_sha2_password` and `mysql_native_password`, COM_QUERY, result
  sets, COM_QUIT), over `Target.Dial`. Measured on 2026-11-10:
  `go-mysql-org/go-mysql` (MIT) adds 1.87 MB to `bin/dock` and 0.5 MB of
  idle memory from its package init, and main is already at 67.0 MB of
  its 67.8 MB budget, so it doesn't fit. `go-sql-driver/mysql` is smaller
  but MPL-2.0, which [0008](0008-keep-doors-open.md) rules out. The
  engine needs nothing else: no prepared statements, no binary protocol,
  no pool. Measured with it: `bin/dock` 67.0 MB to 67.3 MB, idle memory
  unchanged.
- Text protocol only, with one literal renderer for pages, filters and
  changesets (the review shows the SQL that runs). Strings render as
  `'...'` with quotes doubled, or as a `_utf8mb4 X'...'` hex literal when
  they hold a backslash or a control byte, so a literal means the same in
  every `sql_mode`; numbers render bare (MySQL compares a number column
  with a quoted string as a double). Sessions are UTC and utf8mb4 with 1 s
  lock waits (row and metadata); `CLIENT_FOUND_ROWS` makes updates count
  matched rows. Multi-statements are never enabled.
  Reads time out with `max_execution_time` (MariaDB
  `max_statement_time`) and are cancelled with `KILL QUERY`.
- Roles are users at `'%'` with `MAX_USER_CONNECTIONS 3` (two cached
  connections and one that sends `KILL QUERY`): the reader has
  `SELECT, SHOW VIEW ON db.*`, the writer DML, the masked reader column
  grants synced per table, the monitor `PROCESS` and `SELECT ON
  performance_schema.*`. None can read `mysql.*`.
- Health from `performance_schema` (statement digests, whose text has no
  literals; `QUERY_SAMPLE_TEXT` is never read) and `information_schema`.

### Redis and Valkey

- Our own RESP2 client: pipelining and deadlines are all the engine
  needs; libraries bring pools and goroutines.
- Every command Dock sends is O(1) or bounded by a count Dock chose:
  `SCAN` with `COUNT`, values paged 100 at a time, `MEMORY USAGE` with 5
  samples, whole-value console commands only under 10 000 elements, never
  `KEYS`, blocking, scripting or pub/sub. Readers use `CLIENT NO-TOUCH`.
- ACL users with explicit command lists compiled into Dock (categories
  shift between versions). Redis keeps ACL users in memory, so a login
  refused for a set-up database is `ErrNoPrivilege` and the panel's grants
  sync recreates them from the stored passwords.
- Sensitive key patterns (globs) replace sensitive columns. ACLs can't
  deny a pattern, so masking is the engine's: matching keys show masked
  names and values until an audited reveal, and masked callers get no
  console while any pattern is marked. Key names need level `read`.
- Writes are typed key changes in the same changesets: `WATCH`, compare
  the old values, `MULTI`/`EXEC`. No rename, flush or pattern deletes.
- Health: overview from `INFO`, queries from `INFO commandstats` and the
  slow log, sessions from `CLIENT LIST`, and a `keys` view of sampled
  prefixes. Cancelling a running command is impossible in Redis and is
  refused; ending a client is `CLIENT KILL`.

### Surface, templates, tempoBase

- Existing endpoints, commands and tools serve every engine. Redis adds
  `GET /data/keys`, `GET /data/keys/value`, `PUT /data/sensitive-keys`,
  `dock db keys`, `dock db key`, and MCP `data_keys` and `data_key`.
- UI: one panel with a per-engine descriptor (tabs, dialect, Health
  labels). Redis' Keys tab is mock B: a flat key grid filtered by MATCH
  and TYPE with a value drawer; Enter opens a key full width, and a click
  on a key segment sets the match.
- No MySQL template now (templates are parked): image detection and
  compose imports already give working MySQL services. The Redis template
  gets `database = redis` and automatic setup.
- tempoBase registers Postgres only for now (its standalone build is low
  priority); the engines are shared, so adding them is a registration
  each and a look at its 21 MB budget.

Rollout, one PR each: the engine registry and connection cache; MySQL
browse, query and changes; MySQL health; Redis keys; Redis changes and
health.

## Consequences

- No new process, timer or idle work; each engine costs something only
  while used.
- Dock creates users in MySQL and ACL users in Redis, visible in their
  own listings, like its Postgres roles.
- Redis masking is weaker than Postgres' column grants: it is enforced
  by Dock, not the server, which is why masked callers lose the console.
- Dock owns a small MySQL protocol client (no new dependency), tested
  against MySQL and MariaDB servers in CI.

## Amendment (2026-11-10): Redis in two steps

Redis' keys land in two PRs: the engine, the keys API (`GET /data/keys`,
`GET /data/keys/value`, in Dock and tempoBase alike), `dock db keys` /
`dock db key` and MCP `data_keys` / `data_key` (Dock only: tempoBase has no
Redis) first, with the Data tab showing only the command console; then the
key browser (mock B) and sensitive key patterns. Console commands run in
`DataQueryRequest.db`. The `services.database` CHECK of migration 0010
only allowed `postgres`, so `mysql` failed to save; migration 0087 widens
it to every engine.
