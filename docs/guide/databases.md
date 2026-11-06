# Databases and backups

Postgres and Redis come as templates: a regular service with a pinned
image, a volume, generated credentials and defaults tuned for a small
server. Every service with a volume is backed up daily with restic, and a
database restores into a new service, never over the live one; once it
checks out, the environment switches to it in one step. Design:
[ADR 0019](../decisions/0019-templates-and-backups.md) and
[ADR 0061](../decisions/0061-one-restore-model.md).

## Add a database

```sh
dock service create db --template postgres     # or: --template redis
dock env set -s api DATABASE_URL='${{db.DATABASE_URL}}' --deploy
```

In the UI: **add database** next to an environment on the project page.

| | Postgres 17 | Redis 8 |
|---|---|---|
| Others reference | `${{db.DATABASE_URL}}` | `${{cache.REDIS_URL}}` |
| Variables it gets | `POSTGRES_USER` (`postgres`), `POSTGRES_PASSWORD` (generated), `POSTGRES_DB` (`app`), `DATABASE_URL` | `REDIS_PASSWORD` (generated), `REDIS_URL` |
| Memory limit | 512 MB (`shared_buffers` 128 MB, 50 connections) | 256 MB (`maxmemory` 200 MB, `noeviction`, append-only file) |
| Volume | `data` at `/var/lib/postgresql/data` | `data` at `/data` |

- Creating a template service deploys it at once. Afterwards it is an
  ordinary service: change its memory or start command, roll it back, and
  so on.
- It publishes no port. Other services reach it by name on the
  environment's private network; the health check connects there too.
  From your laptop use the Data tab, `dock db`, or your own client through
  [`dock connect`](data.md#connecting-from-your-machine).
- Postgres templates get the [Data tab](data.md) set up automatically.
- Changing `POSTGRES_PASSWORD` later doesn't change the database's
  password (Postgres only reads it when the volume is first initialised).

## Backups

| What | How |
|---|---|
| Postgres template | `pg_dump -Fc`, in a short-lived container of the same image (so the tool matches the server), streamed into restic |
| Redis template | `redis-cli --rdb`: a consistent snapshot over the replication protocol |
| Any other service with a volume | The volume's files, mounted read-only. A database writing while it is copied can leave an inconsistent copy: use a template for databases |

Backups run daily at 03:00 (the control plane's local time) and after a
server that was offline at that time reconnects. One runs at a time.

```sh
dock backup list -s db             # schedule and the latest runs, with snapshot IDs
dock backup run -s db              # now, and wait for it
dock backup schedule -s db --hour 4
dock backup schedule -s worker --off
```

The service's **Backups** tab shows the same, with Run now and Restore. A
failed backup is a problem on the project page until the next one succeeds.
**View log** on a run (or `dock backup log <id> -s db`) shows what its job
printed: the last 16 KB, with the repository's and database's secrets
masked, for a service's newest 20 runs, to anyone with deploy access.

### Where backups go

Out of the box each server keeps a local restic repository in a Docker
volume: **not off-box**. Losing the server loses them. Send them to any
S3-compatible bucket (AWS S3, Hetzner, Cloudflare R2, Backblaze B2, MinIO,
Garage): add it once as a storage connection, then move the backups there
([ADR 0039](../decisions/0039-backups-on-storage-connections.md)):

```sh
printf %s "$SECRET" | dock storage add hetzner-backups --endpoint https://hel1.your-objectstorage.com \
  --region hel1 --bucket backups-dock --access-key-id "$KEY_ID" --secret-access-key-stdin
dock backup move --storage hetzner-backups          # folder dock-backups in the bucket
dock backup status                                   # where the backups are, and how the move went
```

The console has the same: the Storage page, and **Move backups** on the
Backups page. A move copies every repository holding backups into the
bucket, checks the copy, points each backup at its copy (restores keep
working), makes the bucket where new backups go, and then empties the old
repositories. Backups and restores wait while it runs; a failure before the
switch changes nothing. Between two buckets, both connections need the same
endpoint and keys. A repository URL set with `dock backup configure`
before this became a storage connection on update.

The repository password is made on first use; **store it** in your password
manager: without it nobody can read the backups, including you after losing
the server. Operators show it with `dock backup status --reveal`.

Retention per service: 7 daily, 4 weekly and 6 monthly snapshots
(`--keep-daily`, `--keep-weekly`, `--keep-monthly`). The default hour is
`--hour`.

A store on your LAN (MinIO on a NAS) is a private address: allow it with
`[network] allow_private` in `config.toml`, since backup jobs run on the
app network.

## Restore

Backups and snapshots (below) are both **restore points** of a service.
The **Backups** tab lists them together, and one Restore action restores
either:

- A **template database** restores into a **new** service, `db-restored`
  by default, and `db` keeps running untouched. Look at the data (the Data
  panel works on it), then **Use db-restored**: every `${{db.KEY}}`
  reference in the environment's variables becomes `${{db-restored.KEY}}`
  and the services using it redeploy. Switching back is the same action
  the other way, and `db` stays until you archive it.
- **Any other service** restores a snapshot **in place**, after a safety
  snapshot (see [Snapshots](#snapshots)): nothing refers to it by name.

From the CLI:

```sh
dock backup restore -s db --snapshot 4cf53263          # into db-restored
dock volume snapshot restore 12 -s db                  # the same, from a snapshot
dock service switch-references db --to db-restored --dry-run   # who would redeploy
dock service switch-references db --to db-restored     # use it
dock service switch-references db-restored --to db     # switch back
```

A backup's restore gets new credentials, a snapshot's keeps the
snapshot's. The switch needs every referenced key on the new service, and
is refused while the environment has other staged changes (apply or
discard them first). Literal URLs naming the host (`postgres://db:5432`)
are not rewritten; use references so a switch catches them.

Restores read only the repository backups go to now. A backup whose
snapshot is in another one (its storage connection's endpoint or bucket
changed, say) says so on the Backups tab, Restore is off for it, and it is
restored by hand like the backups below.

Backups of other volumes are restored by hand for now: with the repository
password, `restic -r <repository> restore <snapshot> --target <dir>`
(locally, the repository is the `dock_backups_*` volume on the server).

## Snapshots

A snapshot is a quick copy of a service's volumes on its own server, for
the minutes before something risky: a migration, a bulk edit, a
`dock run` ([ADR 0047](../decisions/0047-volume-snapshots.md)). It works for
any service with a volume.

```sh
dock volume snapshot -s db --note "before 2.0"   # take one now
dock volume snapshot list -s db                  # snapshots and restores
dock volume snapshot restore 12 -s db            # into a new service db-restored
dock volume snapshot restore 12 -s db --in-place # replace db's data
dock volume snapshot delete 12 -s db
```

The service's containers are paused (not stopped) while its volumes are
copied: requests wait for a few seconds instead of failing, and the copy
is what a power cut at that moment would leave, which databases recover
from. The **Backups** tab lists them with Take snapshot, Restore and
Delete.

| | Snapshot | Backup |
|---|---|---|
| Where | the service's server | a restic repository, ideally off-box |
| When | by hand, and before risky actions | daily |
| Kept | the newest 5 per service, at most 7 days | 7 daily, 4 weekly, 6 monthly |
| Restore | seconds: files copied back | a dump loaded into a new service |

A template database restores into a **new** service, with the
credentials of the snapshot's data, and the original keeps running; switch
to it as in [Restore](#restore). Any other service restores **in place**:
it stops, its current data is snapshotted first (the *safety snapshot*,
which undoes the restore), the snapshot is copied in, and it starts again.
Deploys wait meanwhile. (`--in-place` also works for a template database,
for scripts; the console doesn't offer it.)

Two actions take one by themselves, and stop if it fails:

| Before | When | Skip it |
|---|---|---|
| A pre-deploy command | the service's `pre_deploy_snapshot` is on (off by default): every deployed stateful service in the environment, since a migration may touch any of them | `dock service update --pre-deploy-snapshot=false` |
| `dock run` | it mounts the service's volumes with `--volumes write` | `dock run --no-snapshot` |

Snapshots count against the server's disk: a snapshot that would leave
less than a tenth of the disk free is refused. Change retention with
`[backup] snapshot_keep` and `snapshot_days` in `config.toml`.

## Permissions

Backups are the platform's: they run on their own, and the platform's
operator decides where they go and when.

| Action | Needs |
|---|---|
| See backups, snapshots and settings | read |
| Back up now, restore; take, restore and delete snapshots | deploy |
| Change a service's schedule, configure retention, move the backups, reveal the repository password | operator |
