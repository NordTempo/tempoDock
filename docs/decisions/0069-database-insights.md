# 0069: Database insights

Status: accepted (Saile's mock pick B, 2026-11-10). Builds on
[0016](0016-database-panel.md) and [0024](0024-tempobase-standalone.md);
amends [0019](0019-templates-and-backups.md) once query stats land.

## Context

The Data panel shows what is in a database, not how it is doing. When an
app gets slow or a deploy fails with "too many clients", the questions are
which queries cost the most, who holds the connections, what is blocked
and by whom, which tables bloat, which indexes are dead weight or missing.
Dock already has the connection, the roles and the access policy, so most
of the answer is fixed, read-only SQL over the database's own statistics.

## Decision

- **A Health tab in the Data panel**, in Dock and tempoBase, with views
  that are also the API's: overview, queries, sessions, tables, indexes.
  One endpoint, `GET .../data/insights?view=...`, returns `DataInsights`:
  the requested views, hints (what needs a look, warnings first, with a
  statement to copy where one helps) and `unavailable` (why a view is
  empty or partial). The shape is engine neutral: opaque session and
  statement IDs, hint kinds, per-view reasons. MySQL and Redis fill the
  views they have.
- **A fourth role, `dock_monitor`** (`tempobase_monitor`): LOGIN, member
  of `pg_monitor` only, no table privileges, read-only by default, and it
  only ever runs SQL compiled into the engine. Other roles' sessions and
  statement texts need `pg_read_all_stats`; giving that to `dock_reader`
  would show every session's literals to anyone with SQL access, past the
  sensitive-column masks. Its password is derived from the reader's
  (HMAC), like the masked reader's: no stored secret, rotates with setup.
  Setup creates it; databases set up before it get it on first use,
  through the owner path grant syncs already use. If the owner can't grant
  `pg_monitor` (some managed services), the role exists without it and the
  views say texts are hidden.
- **Live, on demand, nothing stored.** No snapshots, no ticker. Counters
  already accumulate since their reset or the server's start, and each
  result says since when. Concurrent identical reads of a database share
  one trip to it.
- **Cheap on a busy database.** Every read runs as the monitor in `BEGIN
  READ ONLY` with a 2 s statement timeout and 200 ms lock timeout, row caps
  in SQL, the current database only. `pg_blocking_pids` runs only for
  sessions waiting on a lock. Size functions are skipped for relations an
  ACCESS EXCLUSIVE lock is held or queued on (they would wait in that
  queue); those show the catalog's estimate. Each view runs in a
  savepoint, so one that fails leaves the others. Measured on Postgres 17:
  2 to 16 ms per view.
- **Permissions are the existing levels.** Overview, tables and indexes
  need `schema`; queries and sessions, which carry statement texts, need
  `read`. Texts are redacted (literals become `?`) for callers who see
  sensitive columns masked, and for everyone when they mention a password.
  Looking at texts is audited as `data.insights`, throttled like
  `data.browse`.
- **Deploy diagnosis** reuses `databasePressure` in core: connections
  against the limit, waiting and idle-in-transaction counts, the oldest
  transaction. Counts only, so no actor and no audit. A login refused for
  too many connections (53300) is `full`, which is itself the answer.
- **Agents** get one tool, `data_insights`, with tighter caps (10
  statements by default, non-idle sessions, texts at 1 KB).

Also built (second and third PRs, 2026-11-10):

- **Query stats.** The Postgres template adds `-c
  shared_preload_libraries=pg_stat_statements -c
  pg_stat_statements.track_utility=off` (utility statements are noise and
  carry passwords in clear), and Dock creates the extension when a
  template release goes active. Existing template databases get "Turn on
  query stats", which stages the start command change; it applies with the
  next deploy (a restart), never on its own. Elsewhere creating the
  extension is an explicit admin action, like setup. Every activation of a
  template release creates the extension if its command loads the
  library and it is missing: one owner connection, nothing otherwise.
- **Actions**: cancel a query (`write`; tokens never on production), end a
  session (`write`, production confirms with the environment's name),
  reset query stats (admins). They run as the owner on one uncached
  connection: template apps connect as a superuser, which
  `pg_signal_backend` can't signal. A session ID binds the process ID to
  its backend's start, and a cancel also binds the query's start, so a
  reused process ID is never signalled. Not askable through
  `request_access`: approved later, they would point at a query long gone.
- **No "create index".** Indexes belong in the app's migrations; Dock hands
  over the evidence and the statement. DDL stays out of the panel (0016).

## Consequences

- No new process, table, migration or timer; the feature costs something
  only while someone looks, and one connection that closes a minute later.
- Dock creates one more role in user databases, visible in `\du`.
- The queries view is empty until `pg_stat_statements` is loaded and
  created; the hints say how.
- History ("what was slow last night") isn't answered beyond counters
  since restart. If it is wanted, it gets its own ADR: hourly snapshots of
  the top statements on the statistics tick.
