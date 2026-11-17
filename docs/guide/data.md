# Data

Browse, query and change the data in a database service: the **Data** tab
on the service page, or `dock db` in the terminal. Both use the same API
and the same permissions, and everything is in the audit log. The design
is [ADR 0016](../decisions/0016-database-panel.md); MySQL and MariaDB are
[ADR 0072](../decisions/0072-mysql-and-redis-in-the-data-panel.md).

Postgres 14 or newer, MySQL 8.0 or newer and MariaDB 10.6 or newer, Redis
7.0 or newer and Valkey 7.2 or newer. Redis has keys, not tables: see
[Redis and Valkey](#redis-and-valkey); its Health view comes later.

## Is it a database?

Dock detects Postgres from the image (`postgres`, `postgis/postgis`,
`pgvector/pgvector`, `timescale/timescaledb*`, `bitnami/postgresql`,
`supabase/postgres`), MySQL and MariaDB from theirs (`mysql`,
`mysql/mysql-server`, `mariadb`, `bitnami/mysql`, `bitnami/mariadb`,
`percona`), or from the template that created the service. For anything
else, say so:

```sh
dock db settings --database postgres          # or mysql, none, or detect
dock db settings --url-variable DATABASE_URL  # where the connection URL is
```

Settings apply at once and never create a release. Connection details come
from the active release: the variable `--url-variable` names (e.g.
`api.DATABASE_URL` for another service's), else the service's own
`DATABASE_URL` (MySQL: also `MYSQL_URL`, a `mysql://` URL), else the
image's own variables: `POSTGRES_USER`, `POSTGRES_PASSWORD` and
`POSTGRES_DB`; for MySQL and MariaDB `root` with `MYSQL_ROOT_PASSWORD` (or
`MARIADB_ROOT_PASSWORD`) and `MYSQL_DATABASE` (or `MARIADB_DATABASE`). A
MySQL URL or service without a database covers every user database.
The panel always dials the service's own container on its private network;
the database never gets a public port.

## Set up access

Dock never browses as the database owner. An admin creates two
least-privilege roles once (template databases get them automatically):

```sh
dock db setup          # asks first; --yes skips the question
dock db status         # engine, version, status, your access and why
```

`dock_reader` reads everything (`pg_read_all_data`, read-only by default) and
runs all browsing and SQL. `dock_writer` can change rows but not the schema
and only commits changesets. Their passwords are generated, stored encrypted
in Dock and never shown; running setup again rotates them. They show up in
`\du`. Row-level security still applies to them.

On MySQL and MariaDB they are users at `'%'` (Dock connects from the
private network's gateway): `dock_reader` with `SELECT` and `SHOW VIEW` on
the app's databases, `dock_writer` with `SELECT, INSERT, UPDATE, DELETE`,
neither with anything on `mysql.*`, and each limited to 3 connections
(`MAX_USER_CONNECTIONS`). Setup needs an owner that may create users with
grants, such as `root`: a service with only `MYSQL_USER` or a random root
password needs `--url-variable` pointing at a root URL. A database created
after setup is covered by running setup again.

## Browse and query

```sh
dock db tables
dock db schema orders                                  # columns, keys, indexes
dock db rows orders -w status=paid -w total=gt:100 --order -created_at
dock db rows orders --after <cursor>                   # the next page
dock db query "select status, count(*) from orders group by 1"
dock db query -f report.sql --csv > report.csv
dock db history                                        # your past queries
dock db saved save open-orders "select * from orders where status = 'open'"
dock db saved run open-orders
```

- `--csv` writes values as they are, for scripts. A spreadsheet runs a
  cell that starts with `=`, `+`, `-` or `@` as a formula, and a database
  holds what its app's users typed: the web UI's **CSV** button (and
  Activity's **Export CSV**) puts an apostrophe before such a cell, so it
  shows as text; plain numbers stay numbers.
- Pages are keyset pages (100 rows, at most 1 000), never `OFFSET`; the
  cursor for the next one is printed on stderr. Row counts are estimates
  (`~N`); tables never analysed are counted exactly when small, else `?`.
  `--count` counts exactly.
- The table list is cached for a minute. A table you name that it lacks
  (one a migration just made) is looked up again at once, and a restore
  into the service drops the cache.
- Filters: `col=value`, `col=op:value` (`eq ne lt lte gt gte like ilike in`),
  `col=is_null`, `col=not_null`. Anything fancier is SQL.
- `query` runs one statement in a read-only transaction: 1 000 rows by
  default (`-n`, at most 100 000), a 10 s timeout (`--timeout`, at most
  60 s), at most 64 MB, cells over 8 KB cut. Results stream as they arrive;
  `--json` prints the API's NDJSON frames (header, rows, then end or error).
- Writes in `query` fail: a statement that isn't a read is refused before
  it runs (exit 2), and a write inside a read (`WITH ... DELETE`, a
  function that writes) fails in the read-only transaction (exit 3).
  Selecting a sensitive column your access masks fails too (exit 3).
  Change data with changesets.
- MySQL and MariaDB: `query` runs reads only (`SELECT`, `WITH`, `SHOW`,
  `DESCRIBE`, `EXPLAIN`, `TABLE`, `VALUES`), never locking reads (`FOR
  UPDATE`, `FOR SHARE`) or executable comments (`/*! */`); named locks a
  query takes are released after it. A table without a primary key or a
  unique `NOT NULL` index pages by offset, up to 10 000 rows; past that,
  use `query`. 64-bit integers and decimals are exact strings, times are
  UTC.

## Redis and Valkey

Dock detects `redis`, `valkey/valkey`, `bitnami/redis`, `bitnami/valkey` and
`redis/redis-stack-server` images, and the Redis template marks its service
itself and sets up access when it starts. The owner is the default user
with `REDIS_PASSWORD` (or the user and password of `REDIS_URL`, whose
`/N` names the logical database); standalone servers only, not cluster
mode.

```sh
dock db keys 'user:*' --type hash      # one page of keys, found with SCAN
dock db key user:1042                  # type, TTL, size and a page of the value
dock db query 'ZRANGE board 0 9 REV WITHSCORES' --db 0
```

- Keys come from `SCAN` steps, never `KEYS`: a page is about 100 keys,
  at most 10 steps or 50 ms, so it may hold fewer and still go on. Sizes
  are `MEMORY USAGE` with 5 samples, an estimate for big values.
- A value comes a page at a time: hash fields and set members by
  `HSCAN`/`SSCAN`, list elements and sorted set members by index, stream
  entries newest first, strings up to 8 KB (`--full`: 1 MB).
- `query` runs one read command (`GET`, `HGETALL`, `ZRANGE`, `XRANGE`,
  `SCAN`, `TYPE`, `TTL`, `INFO`, ...). Commands that read a whole value
  run only up to 10 000 elements (1 MB for strings), else they say what to
  use instead; nothing that writes, blocks, scripts or runs `KEYS`.
- The Data tab's **Keys** view is one grid of keys (type, length,
  sampled size, TTL) filtered by match, type and database, with the open
  key's value on the side; Enter opens it full width, and clicking a part
  of a key's name (`user` in `user:1042`) shows the keys with that prefix.
  **Commands** is the console.
- Key names need level `read`, like rows: they often hold IDs and
  emails.
- Sensitive keys are patterns an admin marks (`dock db sensitive set
  --keys 'session:*'`, which replaces the list `dock db sensitive list`
  shows, or Sensitive keys in the Keys view). Callers who
  see sensitive values masked get matching keys by pattern, not name, and
  no console (Redis ACLs can't deny a pattern, so the console could read
  what the list masks); the others read them with an audited reveal
  (`dock db key ... --reveal`).
- Dock's users are ACL users with fixed command lists: `dock_reader`
  reads, `dock_writer` commits changesets, `dock_monitor` reads the
  server's statistics, never keys or `CONFIG`. Redis keeps ACL users in
  memory, so a restart drops them; Dock creates them again as the owner
  on the next use. Browsing never changes eviction order
  (`CLIENT NO-TOUCH`).

## Change data

Changes are changesets: row inserts, updates and deletes committed in one
transaction. Each update and delete matches the primary key and the values
you saw, so if someone changed the row meanwhile nothing is written and the
conflict is listed (exit 5). At most 500 rows per changeset; tables need a
primary key.

```json
{
  "message": "fix the total of order 1041",
  "changes": [
    {"op": "update", "table": "public.orders", "key": {"id": "1041"},
     "old": {"total": "120.00"}, "new": {"total": "102.00"}},
    {"op": "delete", "table": "public.carts", "key": {"id": "77"}}
  ]
}
```

Values are strings in the database's text format, or `null`. Binary values
(Postgres `bytea`, MySQL `BLOB`, `VARBINARY`, `BIT`) are `\x` hex.

```sh
dock db apply -f changes.json --dry-run                 # the exact SQL, nothing stored
dock db apply -f changes.json                           # commit (level write) or propose
dock db apply -f changes.json --confirm production      # production asks for its name
dock db changes list --status pending
dock db changes show 12
dock db changes commit 12 --confirm production          # admins approve proposals
dock db changes reject 12 --reason "use the refund flow"
```

With level `propose`, `apply` stores the changeset as pending and an admin
commits or rejects it. Pending changesets expire after 7 days.

### Redis keys

Redis changesets hold `key_changes` instead, all in one logical database.
Each one carries `old`, the key's type and the value you saw (`null`: not
there); the commit `WATCH`es the keys, compares, and runs the writes in
one `MULTI`/`EXEC`, so a key changed meanwhile writes nothing and is
listed as a conflict.

```json
{
  "message": "give 1042 the pro plan",
  "key_changes": [
    {"op": "hset", "db": 0, "key": "user:1042", "field": "plan",
     "value": "pro", "old": {"type": "hash", "value": "free"}},
    {"op": "expire", "db": 0, "key": "session:9f2", "ttl_ms": 3600000,
     "old": {"type": "string"}},
    {"op": "rpush", "db": 0, "key": "jobs", "value": "reindex"}
  ]
}
```

| Op | Does | `field` | `value` |
|---|---|---|---|
| `set` | Set a string (keeps its TTL) | | the string |
| `delete` | Delete the key | | |
| `expire` | `ttl_ms` milliseconds to live, `-1` removes the TTL | | |
| `hset`, `hdel` | Set or remove a hash field | the field | the value (`hset`) |
| `sadd`, `srem` | Add or remove a set member (`old.value` is `"1"` when present) | the member | |
| `zadd`, `zrem` | Add or remove a sorted set member | the member | the score (`zadd`) |
| `lset` | Set a list element | its index | the value |
| `rpush`, `lpush` | Append or prepend to a list (no `old`) | | the value |
| `lrem` | Remove the first element equal to `field` | the element | |

In the Keys view, **Edit** (E) turns the open key's value into editable
rows: change a value, remove a row, add one, set the TTL or delete the
key. Staged changes across keys of one database are reviewed beside it,
with the commands that run. Values are text up to 512 KB; change binary
values and keys named in hex with redis-cli over
[`dock connect`](#connecting-from-your-machine). No rename, flush or
pattern deletes.

## Who may do what

A **data access policy** decides, per subject and environment kind, a level
and whether sensitive columns are visible:

| Level | Allows |
|---|---|
| `none` | Nothing |
| `schema` | Tables, columns, relations; no rows |
| `read` | Browsing and read-only SQL |
| `propose` | Changesets an admin approves |
| `write` | Committing one's own changesets |

Subjects are users with role `read`, users with role `deploy`, and `tokens`.
Admins can always do everything, and only they approve proposals, run
setup, mark sensitive columns and change the policy. Built-in defaults:

| Subject | Non-production | Production |
|---|---|---|
| `role:read` | `none` | `none` |
| `role:deploy` | `read` | `none` |
| `tokens` | `none` | `none` |

Sensitive columns are masked for all of them. Change the policy as a
default, per project or per environment; the most specific rule wins:

```sh
dock db policy show                                   # the default
dock db policy show -p shop -e production             # what applies there, and from where
dock db policy set deploy.production=read -p shop
dock db policy set tokens.non-production=read,masked
dock db policy set deploy.production=inherit -p shop  # remove a rule
```

`dock db status` shows your effective access with the steps that decided it.

## How the database is doing

The Data tab's **Health** view shows it: an overview with what needs a
look, then the queries, sessions, tables and indexes, each with details on
the side. It reads when you open it and when you press R; Follow on
Sessions refreshes every 5 seconds while the page is visible, for up to
10 minutes. "Explain in SQL" opens a statement's generic plan in the SQL
tab. The same from the terminal:

```sh
dock db health                    # connections, cache, size, and what needs a look
dock db queries --order mean      # slowest statements (pg_stat_statements)
dock db sessions                  # running, idle in a transaction, waiting, and who blocks
dock db storage --indexes         # sizes, scans, dead rows, unused and duplicate indexes
```

Dock reads the database's own statistics when you ask, as `dock_monitor`, a
role that is a member of `pg_monitor` and can't read table data. Nothing is
stored and nothing runs while nobody looks. Every read has a 2 second
timeout and skips the exact size of a table someone holds an exclusive lock
on, so it never waits in that lock's queue. Counters count since their last
reset or the server's start.

The overview, tables and indexes need level `schema`; queries and sessions
show statement texts and need `read`, with values redacted when your access
masks sensitive columns (texts that mention a password are redacted for
everyone). Looking at texts is audited as `data.insights`, like browsing.
Hints come with a statement to copy (`DROP INDEX CONCURRENTLY ...`,
`VACUUM (ANALYZE) ...`): put it in a migration or run it yourself; Dock never
does.

The queries view needs `pg_stat_statements`. New Postgres template
databases have it on. For one created before, "Turn on query stats" in the
Queries view (`dock db queries enable`) stages the start command change; it
goes live with your next deploy, which restarts the database for a few
seconds, and Dock creates the extension then. Dock never applies it on its
own. Other Postgres services need `-c shared_preload_libraries=pg_stat_statements`
in their start command, then an admin creates the extension the same way.
Without `pg_monitor` (some managed services refuse the grant) the monitor
still reads sizes and counts, but not other roles' statement texts; the
views say so.

On MySQL and MariaDB the monitor is a user with `PROCESS` and read access
to `performance_schema` (and `mysql.innodb_index_stats`, for index sizes),
created on first use like on Postgres. Queries are the server's statement
digests, whose text already has `?` for every value; they aren't per user,
so Dock's own statements show too. Tables and indexes count rows read by
full scans and through each index since the server started. MariaDB ships
with `performance_schema` off: add `--performance-schema=ON` to the start
command and deploy to get the queries view and index use (sessions, sizes
and the overview work without it). Cancel query is `KILL QUERY`, End
session `KILL CONNECTION`.

Redis and Valkey have Overview, Commands, Clients and Keys. The overview
is `INFO`: memory against `maxmemory` and what happens at the limit,
evicted keys, hit rate, commands per second and keys. Commands are `INFO
commandstats` (calls and time per command, never arguments), with each
command's slowest call from the slow log; Redis always counts them. Clients
are `CLIENT LIST`: a client in a `MULTI` shows as idle in a transaction, one
blocked in `BLPOP` and the like as waiting. Keys groups a sample of each
database's keys (about 1,000 in all, sized with `MEMORY USAGE`) by prefix
(`user:`), scaled to the key count, with the biggest key of each; it needs
level `read`, like browsing (`dock db storage --keys`). Hints flag memory
near the limit, evicted keys, failing snapshots or append-only writes, and
sampled keys over 16 MB. Redis can't stop a running command, so there is no
Cancel; End client is `CLIENT KILL`. Reset is `CONFIG RESETSTAT` and
`SLOWLOG RESET`; Redis keeps no time for it, so there is no "counting
since".

From a session's side pane, "Cancel query" stops its running statement
(the app gets an error, its connection stays) and "End session" drops its
connection and rolls back its open transaction, the usual fix for a
session idle in a transaction that blocks others. Both need level `write`,
so by default only admins can on production, and tokens never can there;
ending a session on production asks for the environment's name. They run
as the database owner, are audited as `data.cancel` and `data.terminate`,
and only hit the query you saw: a session that moved on is left alone.
Admins can reset the query stats (`dock db queries reset`).

## Agents

Agents get nothing until you grant it, either in the policy's `tokens` rule
or on the token itself:

```sh
dock token create claude-shop --scope deploy --project shop \
  --data-non-production read --data-production none
dock token update 7 --data-non-production propose
dock token update 7 --data-clear                      # follow the policy again
```

A token never gets more than a user with its scope would, and on production
`write` acts as `propose`: an agent's changes to production data always wait
for a human to approve the exact SQL. Agents use the same commands with
`--json`; see [agents](agents.md).

## Sensitive columns

```sh
dock db sensitive set users.password_hash users.email   # replaces the list
dock db sensitive list
```

Table pages show sensitive columns as `(masked)`. Callers whose policy says
`masked` never see them: their SQL runs as `dock_reader_masked`, which
Postgres doesn't let select them. Callers with `visible` (admins always)
reveal them per table, `dock db rows users --reveal` or Reveal in the Data
tab, and each reveal is audited (`data.reveal`). Their SQL isn't masked;
every statement is in the audit log anyway. The Schema tab suggests
columns to mark from their names (email, password, token, phone, ...).

A view reads its tables with its owner's rights, so masked callers can't
read one that could show a sensitive column: in Postgres a view or
materialized view that uses one (directly or through another view); in
MySQL and MariaDB, while any column is marked, every view. A restored copy
of a database (`dock backup restore`, a snapshot restored as a new
service) keeps the source's marks.

## Connecting from your machine

For psql, TablePlus, pg_dump, redis-cli and the like, `dock connect` opens a
port on `127.0.0.1` and carries every connection to the service's private
port through the API. The service never gets a public port. It works for
any service with a port, on any server, and runs until Ctrl-C. The design
is [ADR 0023](../decisions/0023-connect-tunnels.md).

```sh
dock connect db                        # 127.0.0.1:5432, or a free port if that's taken
psql -h 127.0.0.1 -p 5432 -U postgres app
dock connect db --show-url             # also prints postgres://postgres:...@127.0.0.1:5432/app
dock connect cache --port 6380         # pick the local port
dock connect db -e production --confirm production
```

- A tunnel is raw access: no masking, no changesets, and the SQL you run
  isn't in Dock's audit log. So it needs level `write` with sensitive
  `visible` in that environment. By default only admins have that; an
  admin can grant it, e.g. `dock db policy set
  deploy.non-production=write,visible -p shop`.
- API tokens (agents) never open tunnels to production, whatever their
  scope (`write` acts as `propose` there). Use `dock login` for that.
- Production needs `--confirm <environment>`.
- You log in with the database's own credentials. `--show-url` prints the
  connection URL with its password, pointed at the local port, and is
  audited (`data.reveal`). Without it the password isn't shown.
- Each connection is audited when it opens (`data.connect`, with your
  address) and closes (`data.disconnect`, with its duration and bytes),
  never its traffic. Access is checked per connection; open ones stay open
  when access changes.
- At most 16 open connections per user or token. Closing either end
  closes the other; there is no idle timeout.
- A reverse proxy in front of the API must pass HTTP upgrades on (Caddy
  and nginx with the usual WebSocket settings do).

## Audit

Every query, the first page and the first reveal of a table per person (then
at most every 10 minutes), every changeset step, setup, policy change and
tunnel connection go into the audit log (`dock audit --action data.`), with the SQL kept in the
event's detail.
Your query history is read from it.
