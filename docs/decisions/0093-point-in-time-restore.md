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
| Object layout | `<storage path>/dock-wal/s<service id>/<generation>/<file>@<mtime ms>` in the backups' bucket. The file's mtime is when Postgres finished writing it, so the archive says exactly up to when it holds every commit. Each object is gzip, then AES-256-GCM in 64 KB chunks (counter nonces) under a key of its own: HKDF-SHA256 from the database's key with a random 16-byte salt from the object's header and its file name, the header and name bound to every chunk. The database's key is derived from the archive key (itself from the backup repository password) with the service's ID, and is the only one its archiver and jobs get, so no two databases share a key and no key ever seals two objects. A file archived again after a crash is the same object (same name and mtime); one archived twice with different contents would be two histories, and a restore through it refuses |
| Generation | A new one when PITR turns on, after an in-place restore and after a backup move: each starts with a fresh base backup, so one generation is one unbroken history. Older ones go with their base backups |
| Base backups | A job in the template's image with the database's volume mounted read-only: `pg_backup_start`, a tar of the data directory streamed into restic (`/pgdata.tar`, tagged `dock-s<id>-base`, so restic deduplicates it day to day), `pg_backup_stop` (waiting for its WAL to be archived), `backup_label` last in the tar. One right after turning on, then daily with the service's backup schedule, after the dump, as its own backup run (`kind = pitr`). Dumps stay as they are: they restore across major versions and seed preview copies ([0092](0092-preview-database-copies.md)) |
| Retention | `--days N` (1 to 35, default 7). After each base backup: restic keeps base backups within N + 1 days, then Dock deletes the generation's WAL older than the oldest kept base backup's start, and older generations once none of their base backups are left. Part of the backup run, no timer of its own |
| Restorable window | A list of ranges, oldest first, one per generation: from its first base backup's end to where its archive ends. Any time inside one restores; between two nothing was archived (it was off, or an in-place restore replaced the data). The Backups page shows them, the newest range's end read from the archiver (`GET /status` through the node link) when the page asks. See "The window as ranges" below |
| Restore to a time | `dock backup restore db --to-time 2026-11-17T14:01:00Z [--as NAME \| --in-place]`. Dock picks the newest base backup that ended at or before the time and lists the generation's WAL from its start: a missing segment, or an archive that ends before the time, refuses with the latest time it can reach, and a time between two ranges with the ranges that do restore. `--to-time latest` (`to_latest` in the API) restores the newest moment archived without naming a time, see "Latest" below. A new service (`<db>-restored`, the source's credentials, which the data files hold) gets its volume filled by a job: the base backup unpacked, then Postgres started inside the job with `restore_command` (`/.dock __wal fetch`) and `recovery_target_time`, promoted, and shut down cleanly; then the new service deploys. If no transaction committed between the time and the archive's end, Postgres stops with "recovery ended before configured recovery target was reached"; since Dock checked the archive holds every commit up to the time, the job replays to the end instead, which is the same state |
| In place | `--in-place`: like an in-place snapshot restore ([0047](0047-volume-snapshots.md)): the service stops, a safety snapshot keeps what it holds, the job restores into its volume, it starts again, and a new generation begins with a base backup. MCP and the UI lead with the new service |
| Health | The archiver's health check (`dock-probe http /health`, every 30 s) fails while a finished WAL file has waited over 5 minutes, or the waiting WAL takes over 1 GB or a tenth of the database volume's disk (Postgres keeps every unarchived file, so a stalled archive would fill the disk; this fires long before). Docker's `health_status` events reach core like an app's; an unhealthy archiver opens a service alert ("archiving of db is behind: 4 segments waiting since 14:02; last error: ...") and a healthy one resolves it. A failed base backup is a failed backup, as before |
| Node protocol | 33: `ensure-sidecar`, `remove-sidecar`, and events of sidecars (with their role). Turning PITR on for a database on an older node is refused with the upgrade to make |
| Turning off | Restarts the database without the archive settings (they are on its command line, so a reload can't drop them); Postgres then frees WAL that waited for the archive at its next checkpoint. That is also the way out when the storage is down and the disk fills. The archiver goes once the restart is live, not before: as the old container stops, Postgres hands over its last segment, with the commits of the last minute. Off is stored only then; a restart release that fails is settled by whether the old container stopped, see "A turn-off whose restart fails" below. The window stays and ages out with the daily backups; with no archiver to ask, its end is read from the bucket (one listing of the generation, when the Backups page asks). Turned on again, a time between the old archive's end and the new base backup is refused as a gap |
| Interfaces | API `PUT {service}/backups/pitr` (`enabled`, `days`), the window (`ranges`, and `from` and `through` as their outer bounds), the services using the database and their deploys in `GET {service}/backups`, `to_time` or `to_latest`, and `in_place`, on the backup restore; CLI `dock backup pitr on\|off`, `dock backup restore --to-time TIME\|latest [--in-place]`; MCP `restore_to_time` (into a new service only; turning it on and in place are for people); the Backups tab's timeline strip (mock A). Audit `backup.pitr`, `backup.restore` |

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

### The window as ranges

Amended 2026-11-19. The window was one range, from the oldest base
backup to the archive's end, and after turning archiving off and on
again it spanned the time in between, which a restore then refused.

| Topic | Decision |
|---|---|
| Shape | `pitr.ranges` in `GET {service}/backups`: `[{from, through}]`, oldest first, never null. One range per generation that still has a base backup: generations are what "one unbroken history" already means, so no new state is stored and there is no migration |
| Old fields | `from` and `through` stay, as the outer bounds (the oldest base backup, the end of the newest range): the CLI and the server can be of different versions, and an older CLI reading one window still shows something true about its ends. New clients read `ranges` |
| Cost | The generation being written ends where its archiver says, as before. A generation nothing writes to any more (not the current one, or the current one with archiving off and its archiver removed) can't change, so its end is read from the bucket once per daemon run, one listing, and kept in memory: one entry per ended generation with base backups, dropped when they age out or the service is archived. A database whose archiving never stopped costs no listing; one with a gap costs one per ended generation, once, where turned off it cost one per page view |
| A hole inside a generation | A lost WAL object isn't a range boundary: the range would need every base backup's coverage walked on every read. The restore itself still lists the archive and refuses past a missing segment, naming it |
| Refusal | A time between two ranges is refused with the gap's ends and the ranges that restore, the same ones the page shows |
| Clients | `dock backup list` prints each range; the Backups tab's strip hatches the gap, the heading names each range, and the restore dialog refuses a moment in a gap with its ends and moves a pick to the nearest moment that restores |

### Latest

Amended 2026-11-19. A stopped database refuses a time after it stopped,
and the only way to its last commit was to read the window's end and
type it back.

| Topic | Decision |
|---|---|
| What | `to_latest: true` instead of `to_time`; `dock backup restore --to-time latest`; the "latest" shortcut in the restore dialog. With `to_time` or `snapshot` it is refused |
| Resolves to | Where the archive of the newest base backup ends, read from the bucket when the restore is asked (one listing; the plan lists it again, which a restore can afford). For a running database that is up to a minute behind it (`archive_timeout`); for a stopped one it is its last commit, since Postgres switches WAL and hands the segment over at a clean shutdown |
| Recorded | The restore's `to_time` is the moment it resolved to, so the list, the audit log and the CLI's answer say which it was |
| Not there yet | Nothing archived past the newest base backup (the first minute after turning on) refuses and says to try again in a minute |
| MCP | `restore_to_time` still takes a time: an agent restoring "the latest" of a database it can't see into is rarely what was asked, and tool changes go through the agent eval |

### A turn-off whose restart fails

Amended 2026-11-19. Off was stored when asked, and the archiver removed
when the restart release went live. A restart release that failed left
the archiver running with archiving recorded as off, until the next
release; a daemon restart in between removed the archiver while the old
container still ran with `archive_mode=on`, and Postgres then keeps
every WAL file it can't archive, with the archiving alert closed.

| Topic | Decision |
|---|---|
| When off is stored | When nothing archives any more, not when it is asked. Until then the row says on (the API and CLI answer off, from the daemon's memory, and containers start without the archive settings). A daemon restart in between finds it on with its archiver, which is what the old container needs |
| The restart goes live | Off is stored, the archiver removed, the archiving alert closed. Only the release that turns it off does this: what follows an earlier release going active can arrive after the turn-off was asked, while the old container still archives |
| It fails after the old container stopped | The turn-off stands: Postgres handed over its last segment at that clean stop, the same as when the restart succeeds, so the archive is complete. The failed deploy brings the old release back in a container without the archive settings. The user sees the failed release as usual |
| It fails with the old container still running | Before stopping it (the image, the pre-deploy command, a cancel, the server offline) or when the stop itself failed. The container still archives, so removing its archiver would lose nothing at once and fill the disk later, silently. Point-in-time restore stays on, unbroken, in the same generation; the audit log says the turn-off failed and why, next to the failed release. Turning it off again is the retry |
| Rejected | Removing the archiver anyway (the user asked for off): the old container can't drop `archive_mode` without a restart, which is what just failed. Storing "turning off" in the row: a migration for a state that lasts seconds |
| Left | A daemon that dies while the new container (no archive settings) runs before going live, with a whole WAL segment written in that time, leaves a hole in the generation: restores past it refuse naming the segment, and the next daily base backup starts a clean stretch |

Fixed with it: the row of a database turned on for the first time didn't
record its archiver's server until its restart went live, and a turn-off
in between left the archiver behind.

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
