# 0093: Point-in-time restore for Postgres

Status: accepted (decided by the agent for the coordinator, 2026-11-17).
Amends [0019](0019-templates-and-backups.md) (a second kind of backup for
Postgres templates) and [0061](0061-one-restore-model.md) (a restore to a
time, alongside restore points).

## Context

A Postgres database's backups are a daily `pg_dump` into restic
([0019](0019-templates-and-backups.md)) and volume snapshots on its
server ([0047](0047-volume-snapshots.md)). A restore loses everything
written since the last one: up to a day for a dump. The usual accident
("a migration dropped a column at 14:02") wants the database as it was
at 14:01.

Postgres does this itself: a base backup plus every WAL segment written
since replays to any moment (`recovery_target_time`). What Dock has to
decide is where the WAL goes, who holds the keys, how long it is kept,
and how a restore runs.

| Force | Means here |
|---|---|
| Keys | Backup jobs carry the install's S3 keys and repository password, so they run only in the template's pinned image ([0019](0019-templates-and-backups.md), amendment). The database's own container runs whatever image someone with deploy put there: it must never hold them |
| Idle means idle | No polling in the daemon ([performance](../performance.md)). The archiving runs next to the database, and health travels as Docker events |
| Small servers | Archiving costs a process, uploads and storage for every write; most databases on Dock are small and many don't need it |
| One restore model | Restores go into a new service first and switch references when it checks out ([0061](0061-one-restore-model.md)); in place is explicit and keeps a safety snapshot |
| License | Dock is AGPL; anything it ships or runs must be compatible |

### Tools weighed

| Option | Verdict |
|---|---|
| wal-g (Apache-2.0) | Does all of it, but its release binaries are glibc and cgo (they don't run in the alpine template), a static build is a 40 MB binary Dock would have to build and ship, and its `wal-push` runs in the database's container with the S3 keys |
| pgBackRest (MIT) | C with libssl, libxml2, lz4, zstd and a config file per stanza; it would need its own image, and the same keys problem for `archive-push` |
| `pg_receivewal` in a helper container | Streams over a replication connection, which the template's `pg_hba.conf` (from the official image) doesn't allow from the network, and a replication slot keeps WAL on the database's disk without bound while the helper is down |
| `archive_command` to a small archiver Dock runs next to the database | **Chosen.** Postgres's own archiver drives ordering and retries; the archiver holds the keys; about 400 lines of Go in the dock binary, which runs it |

## Decision

| Topic | Decision |
|---|---|
| Scope | Postgres template databases (`postgres`, `pgvector`): Dock controls their image and start command. Opt-in per database (`dock backup pitr on db --days 7`), never on by default: it restarts the database once, runs a small container next to it, and stores WAL in proportion to writes. Operators turn it on, like schedules (backups are the platform's, [0027](0027-organizations.md)) |
| Needs | Backups on a storage connection ([0039](0039-backups-on-storage-connections.md)). With local repositories it is refused: a WAL archive on the database's own server doesn't survive losing it, and snapshots already cover "a minute ago on this disk" |
| Archiving | While it is on, the database's container starts with `archive_mode=on`, `archive_timeout=60s`, `wal_recycle=off` and `archive_command` running `/.dock-probe wal-push` (added when the container is made, like tracing's variables, not to the start command, so clones, previews and exports don't carry it). `wal-push` PUTs the finished segment to the archiver over the environment network; Postgres marks it archived only once the archiver answers 200, which it does only after the upload |
| Archiver | One container per database (`dock_p<p>_e<e>_s<s>_wal`, role `sidecar`), in the template's pinned image, running the server's own dock binary, bind-mounted read-only (`/.dock __wal serve`), with the S3 keys and the archive key in its environment. Its HTTP API takes `PUT /wal/<name>` (WAL file names only), `GET /health` and `GET /status`; nothing reads WAL back through it. It mounts the database's volume read-only, only to see `pg_wal/archive_status` (what waits to be archived). Docker restarts it; Dock makes it (`EnsureSidecar`) when the database's release goes active and removes it when PITR goes off, the service is archived or deleted, or it moves away |
| Object layout | `<storage path>/dock-wal/s<service id>/<generation>/<file>@<mtime ms>` in the backups' bucket. The file's mtime is when Postgres finished writing it, so the archive says exactly up to when it holds every commit. Each object is gzip, then AES-256-GCM in 64 KB chunks under a key derived (HKDF-SHA256) from the backup repository password, bound to its name. A file archived again after a crash is the same object (same name and mtime); one archived twice with different contents would be two histories, and a restore through it refuses |
| Generation | A new one when PITR turns on, after an in-place restore and after a backup move: each starts with a fresh base backup, so one generation is one unbroken history. Older ones go with their base backups |
| Base backups | A job in the template's image with the database's volume mounted read-only: `pg_backup_start`, a tar of the data directory streamed into restic (`/pgdata.tar`, tagged `dock-s<id>-base`, so restic deduplicates it day to day), `pg_backup_stop` (waiting for its WAL to be archived), `backup_label` last in the tar. One right after turning on, then daily with the service's backup schedule, after the dump, as its own backup run (`kind = pitr`). Dumps stay as they are: they restore across major versions and seed preview copies ([0092](0092-preview-database-copies.md)) |
| Retention | `--days N` (1 to 35, default 7). After each base backup: restic keeps base backups within N + 1 days, then Dock deletes the generation's WAL older than the oldest kept base backup's start, and older generations once none of their base backups are left. Part of the backup run, no timer of its own |
| Restorable window | From the oldest base backup's end to the archive's newest segment. The Backups page shows it, read from the archiver (`GET /status` through the node link) when the page asks |
| Restore to a time | `dock backup restore db --to-time 2026-11-17T14:01:00Z [--as NAME \| --in-place]`. Dock picks the newest base backup that ended at or before the time and lists the generation's WAL from its start: a missing segment, or an archive that ends before the time, refuses with the latest time it can reach. A new service (`<db>-restored`, the source's credentials, which the data files hold) gets its volume filled by a job: the base backup unpacked, then Postgres started inside the job with `restore_command` (`/.dock __wal fetch`) and `recovery_target_time`, promoted, and shut down cleanly; then the new service deploys. If no transaction committed between the time and the archive's end, Postgres stops with "recovery ended before configured recovery target was reached"; since Dock checked the archive holds every commit up to the time, the job replays to the end instead, which is the same state |
| In place | `--in-place`: like an in-place snapshot restore ([0047](0047-volume-snapshots.md)): the service stops, a safety snapshot keeps what it holds, the job restores into its volume, it starts again, and a new generation begins with a base backup. MCP and the UI lead with the new service |
| Health | The archiver's health check (`dock-probe http /health`, every 30 s) fails while a finished WAL file has waited over 5 minutes, or the waiting WAL takes over 1 GB or a tenth of the database volume's disk (Postgres keeps every unarchived file, so a stalled archive would fill the disk; this fires long before). Docker's `health_status` events reach core like an app's; an unhealthy archiver opens a service alert ("archiving of db is behind: 4 segments waiting since 14:02; last error: ...") and a healthy one resolves it. A failed base backup is a failed backup, as before |
| Node protocol | 33: `ensure-sidecar`, `remove-sidecar`, and events of sidecars (with their role). Turning PITR on for a database on an older node is refused with the upgrade to make |
| Turning off | Restarts the database without the archive settings (they are on its command line, so a reload can't drop them); Postgres then frees WAL that waited for the archive at its next checkpoint. That is also the way out when the storage is down and the disk fills. The window stays and ages out with the daily backups |
| Interfaces | API `PUT {service}/backups/pitr` (`enabled`, `days`), the window, the services using the database and their deploys in `GET {service}/backups`, `to_time` and `in_place` on the backup restore; CLI `dock backup pitr on\|off`, `dock backup restore --to-time [--in-place]`; MCP `restore_to_time` (into a new service only; turning it on and in place are for people); the Backups tab's timeline strip (mock A). Audit `backup.pitr`, `backup.restore` |

### Why an archiver container and not the database's own process

Anything in the database's container, environment or start command is
readable by whoever can deploy an image to it. A token with deploy on
staging must not be able to read or delete production's backups, which
share the bucket and keys. The archiver runs only Dock's pinned image
and command, like backup jobs, and what the database can do with it is
add WAL to its own archive. Apps in the environment can reach it too;
they hold the database's superuser password already, so it gives them
nothing new.

### Why the dock binary runs in the archiver and the jobs

Uploading to S3 needs Go's HTTP and TLS stack. In dock-sync, which dock
embeds gzipped, it grew that binary from 0.9 MB to 3.4 MB, past the
binary budget ([performance](../performance.md)). The dock binary on every
server has the stack already and is static, so Dock bind-mounts it
read-only into the archiver and the base backup and restore jobs, where
a hidden first argument (`__wal`, like the Railpack plan child) runs the
archive's commands. The archiver is recreated when dock's version
changes, so it never runs a replaced binary for long. `archive_command`
itself stays in dock-probe, which needs no TLS.

### Why base backups in restic and WAL beside it

Base backups are large and change little from day to day, which is what
restic's deduplication and encryption are for, and they then move,
prune and restore with everything else. WAL segments arrive every minute
while the database writes; one restic snapshot each would mean thousands
of snapshots and a repository lock per minute. So WAL is plain objects
next to the repository, under the same password.

### Why `wal_recycle=off`

`archive_timeout` ends a segment after a minute so a quiet database still
reaches the archive, but a recycled segment's unused tail holds old WAL,
which doesn't compress: a database with one write a minute would store
16 MB a minute. Fresh segments are zero-filled (`wal_init_zero`), so the
tail of a timed-out segment compresses to almost nothing.

## Consequences

- Restore to any moment of the last N days, at most a minute behind the
  database while archiving keeps up.
- Storage: base backups about the database's size (deduplicated across
  days), plus its WAL volume for N days, compressed. S3 requests: about
  one PUT a minute while the database writes, none while it doesn't.
- A database with PITR on runs one more container (about 20 MB) and its
  Docker health check; the daemon does nothing while it archives.
- WAL that can't be archived stays in the database's `pg_wal` and grows
  its disk; the archiver alert says so before the disk alert does.
- A backup move or an in-place restore starts the window over.
- A timeline other than the one the base backup is on (after promoting a
  copy by hand) isn't followed; Dock's own restores start a new
  generation.
