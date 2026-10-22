# Databases and backups

Postgres and Redis come as templates: a regular service with a pinned
image, a volume, generated credentials and defaults tuned for a small
server. Every service with a volume is backed up daily with restic, and a
backup restores into a new service, never over the live one. Design:
[ADR 0019](../decisions/0019-templates-and-backups.md).

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
  From your laptop use the Data tab or `dock db`.
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

### Where backups go

Out of the box each server keeps a local restic repository in a Docker
volume: **not off-box**. Losing the server loses them. Send them to any
S3-compatible store (AWS S3, Cloudflare R2, Backblaze B2, MinIO) once per
install:

```sh
printf %s "$SECRET" | dock backup configure \
  --repository s3:https://s3.eu-central-1.amazonaws.com/my-bucket/dock \
  --access-key-id AKIA... --secret-access-key-stdin
```

The first `configure` prints the **repository password**. Store it in your
password manager: without it nobody can read the backups, including you
after losing the server. Admins can show it again with `dock backup status
--reveal`.

Retention per service: 7 daily, 4 weekly and 6 monthly snapshots
(`--keep-daily`, `--keep-weekly`, `--keep-monthly`). The default hour is
`--hour`.

A store on your LAN (MinIO on a NAS) is a private address: allow it with
`[network] allow_private` in `config.toml`, since backup jobs run on the
app network.

## Restore

```sh
dock backup restore -s db --snapshot 4cf53263 --as db-old
```

This creates a **new** service `db-old` from the same template, with new
credentials, and loads the snapshot into it. `db` keeps running untouched.
When `db-old` checks out, point references at it:

```sh
dock env set -s api DATABASE_URL='${{db-old.DATABASE_URL}}' --deploy
```

and archive the old one when you no longer need it.

Backups of other volumes are restored by hand for now: with the repository
password, `restic -r <repository> restore <snapshot> --target <dir>`
(locally, the repository is the `dock_backups_*` volume on the server).

## Permissions

| Action | Needs |
|---|---|
| See backups and settings | read |
| Run, schedule, restore | deploy |
| Configure the repository, reveal its password | admin |
