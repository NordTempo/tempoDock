# 0092: Previews start with a copy of the base's database

Status: accepted (asked by Saile, 2026-11-17; the coordinator approved the
design, Saile picked mock A for the UI). Amends
[0056](0056-preview-environments.md)'s "Volumes" and
[0077](0077-previews-belong-to-services.md)'s "Seeding".

## Context

A preview's databases started empty, or from the base service's newest
volume snapshot when seeded. Empty makes previews of anything data-driven
much less useful. A snapshot is real data handed to a pull request's
unreviewed code, it is only as fresh as the last one somebody took, and
it went to forks' pull requests too. The Data panel already knows which
columns are sensitive: an admin marks them ([0016](0016-database-panel.md),
#363), and masked readers never see them.

| Force | Means here |
|---|---|
| A pull request runs unreviewed code ([0056](0056-preview-environments.md)) | What reaches the preview's database is readable by that code, in every way its database user can read it |
| Template databases give the app a superuser | Postgres's `postgres` can read the data directory's files (`pg_read_binary_file`, `COPY FROM PROGRAM`); old row versions and WAL are in those files |
| Forks are anyone on GitHub | They get no data unless an admin says so |
| The base may be production | Copying must not pause, lock or write it |
| Idle means idle ([performance](../performance.md)) | Nothing runs until a preview is made or refreshed |
| Databases get big | A cap, a disk check, and a failure that leaves the preview working, just empty |

## Decision

| Topic | Decision |
|---|---|
| Setting | Per service of the base, `service_previews.data`: `empty` (the default), `snapshot` (what seed was: its newest volume snapshot, any service with a volume), `copy` (a copy of the base's live database as the preview is made) or `masked` (that copy with the Data panel's sensitive columns replaced). `copy` and `masked` are for template databases. `data_forks` gives pull requests from forks the data; without it their databases start empty, `snapshot` included |
| Default when copying | `masked`: the UI's control puts it first, and `dock service previews --data` alone means it |
| Who may choose | Admin of the base environment, as for every preview setting. An unmasked choice (`copy`, `snapshot`) or `data_forks` also needs the sensitive columns visible to the actor in that environment: whoever hands unreviewed code the data must be allowed to read it |
| How: Postgres | A dump job in the template's pinned image, on the base's network, reads the live base as its owner. One session exports a snapshot; `pg_dump --snapshot` and the masked tables' `COPY (SELECT <masked expressions>) TO STDOUT` read in it, so the copy is consistent without pausing or locking the base. pg_dump leaves the masked tables' data out (`--filter`). A load job on the preview's network restores into the preview's new database with its new credentials: tables, data, the masked rows, then indexes, constraints and triggers (`--no-owner --no-acl`: the base's roles and passwords are never copied) |
| How: MySQL, MariaDB | The same two jobs. The dump writes the schema without triggers, the data of tables without masked columns (mysqldump), and the masked tables as INSERT statements a generated `SELECT` writes with the masked values in place, then the triggers. The masked tables are read in their own consistent snapshot, a moment before mysqldump's: a row added between the two may be in one and not the other. The load runs with the binary log off |
| How: Redis, Valkey | `copy`: `redis-cli --rdb` from the base, put in the preview's volume as its append-only file's base before it first starts. `masked` with sensitive key patterns starts empty (leaving keys out is not built yet); with none it is a copy |
| Masked values | A nullable column becomes NULL. A NOT NULL one gets a value of its type that says nothing: random text (`masked-<hex>`, hex alone in a short column), 0, false, the epoch, `{}`, an empty binary, a random UUID. A type with no such value stays NULL, and the load fails on its NOT NULL: the database starts empty rather than copy the value. A marked table's partitions and inheritance children are masked too. Materialized views are refreshed from the masked rows. Generated columns are computed again |
| Where it runs | Both jobs on the preview's server through a scratch volume (`<preview db>__copy`), removed after the load, and at the next start after a crash. Copies need the base and the preview on one server for now; across servers the database starts empty and says why |
| Order | A preview with data to copy deploys those databases first, and promotes the rest and starts the pull request's builds only once every copy ended: no pull request code runs before its database holds what it will hold. A Postgres or MySQL database is deployed empty and loaded while it runs; Redis and snapshots are filled before it starts |
| Size | `[previews] copy_max_mb` (2048) caps the base's size; the dump job also needs room for twice it with a tenth of the disk left. Past either, the database starts empty and says why |
| Fail closed | Any failure (fork, cap, disk, another server, base not running, a dump or load error, a restart mid-copy) leaves the database empty: one that held part of a copy is stopped, its volume emptied and started again; if that fails too it stays stopped. Never a part of an unmasked copy, never the base's credentials |
| Progress | `preview_data` (migration 0122) per preview database: mode, `copying`/`copied`/`empty`, the step or the reason, size, masked tables and columns. The API's `Preview.databases`, the preview's line on the project overview, the database's release events and the `preview.data` audit event |
| Refresh | `POST …/environments/{e}/preview/refresh-data` (`dock env previews refresh pr-12 [--service db]`, MCP `refresh_preview_data`, Refresh from base in the UI). Deploy scope on the preview. Each database whose base service copies its data is stopped, emptied and filled again with its current setting, then the preview's other services restart so the pull request's migrations run on the new copy. An asleep preview wakes first. A fork's preview refreshes only what gives forks data |
| Marks | The preview's database gets its base's sensitive columns and key patterns, so its Data panel masks what the base's does |
| Cleanup | The copy is the preview's volume: removed when the preview closes ([0056](0056-preview-environments.md)). Sleeping keeps it |
| Never written | The base is only read: a read-only snapshot transaction, mysqldump with `--single-transaction`, a replication read for Redis. Nothing of the preview is ever written back |
| Migration | 0122: `service_previews.data` and `data_forks`; `seed` services become `snapshot`; `preview_data` |

## Threat model

What a pull request's code can reach in its preview, and why the masked
copy keeps the sensitive values from it:

- **The preview's database, fully.** Template databases give the app
  their superuser (Postgres `postgres`, MySQL `root`). Postgres's can read
  the data directory's files and run programs in the container: any value
  ever written to the preview's database, including old row versions and
  WAL, is readable. So a masked copy must never write a sensitive value
  there, even briefly: the base is read through `SELECT`s with the masked
  expressions, in Postgres and MySQL alike. The values never leave the
  base's database server: not to the scratch volume, the job's output,
  the preview's disk or its logs.
- **Not the base.** The jobs run Dock's scripts in the template's pinned
  image, never the pull request's or the service's own image. The base's
  credentials go to the dump job only; the preview's database gets new
  ones (except `snapshot`, whose data files need the base's, so its
  credentials are copied; forks don't get them without `data_forks`). The
  preview's containers are on the preview's network only.
- **Only what is marked is masked.** Columns nobody marked are copied as
  they are. A value repeated in an unmarked column (a denormalized email,
  a JSON blob, an audit log's text), large objects, and data in other
  Postgres databases of the same server are copied in clear. Masking is as
  good as the marks: mark before turning copies on.
- **Unmasked copies and snapshots are real data.** Choosing one is
  allowed only to someone who may read the sensitive columns themselves,
  and the UI shows it as a warning. Forks never get data unless
  `data_forks` is set, by the same people.
- **Redis keys** are not masked yet: a masked copy of a Redis with
  sensitive key patterns starts empty.

## Consequences

- A preview of a data-driven app is useful at once, and an admin decides
  in one place what it may hold.
- Opening a preview with a copy takes longer: the copy's time comes before
  the pull request's code starts.
- Each copy reads the base like a backup does (one `pg_dump`), at preview
  creation and refresh only; idle cost is unchanged.
- Cross-server copies (through the volume copier,
  [0063](0063-moves-without-downtime-and-server-drains.md)) and masked
  Redis copies are left for later.
