# 0019: Database templates and backups

Status: accepted (decided by the agent, M5); superseded in part by [0039](0039-backups-on-storage-connections.md) (where backups go), [0061](0061-one-restore-model.md) (restores and switching to them); amended by [0069](0069-database-insights.md) (the Postgres template loads `pg_stat_statements` for query stats), [0093](0093-point-in-time-restore.md) (point-in-time restore for Postgres), [0074](0074-image-vulnerability-scanning.md) (a template database's image follows its tag: Update, and new ones start on the newest image), [0110](0110-mongodb.md) (the MongoDB template)

## Context

[0011](0011-stateful-services-and-databases.md) says databases are services
created from templates, backed up with the database's own tool, shipped
off-box with restic, and restored into a new service, never over the live
one. [0016](0016-database-panel.md) says the Data panel detects template
databases and sets up its roles for them automatically. This ADR fills in
how, within the rules that matter here: idle means idle (one timer), no
extra privileges, the same paths for local and remote servers, and nothing a
user has to babysit.

## Decision summary

| Topic | Decision |
|---|---|
| Templates | Defined in Go (`internal/core/templates.go`): Postgres 17 and Redis 8, images pinned by digest. A template is a regular service plus a `template` column. |
| What a template creates | The service (pinned image, tuned start command, memory limit), a `data` volume and generated credentials as staged service variables, then an image deploy that applies them. |
| Health | Template services publish no port (private by default). A new `HealthCheck.Port` probes the container's port over TCP on its environment network, like `Dial`. |
| Data panel | Postgres templates set `database = postgres`; the first time a release goes active, Dock runs data setup as `system` (0016's explicit-setup rule is for non-template databases). |
| Backup jobs | A new runtime call, `RunJob`: a one-off container that runs to completion and is removed. Backups run in the database's **own image**, the template's pinned one (its `pg_dump` / `redis-cli`; see the amendment), on the environment network, with the restic binary mounted read-only from a per-server volume. No exec, no Docker socket, no extra privileges. |
| Formats | Postgres: `pg_dump -Fc` streamed into restic (`--stdin-from-command`, so a failing dump fails the backup). Redis: `redis-cli --rdb` (a consistent snapshot over the replication protocol), then into restic. Generic volumes: file-level restic backup of the volumes mounted read-only, with a warning. |
| Repository | One per install. `dock backup configure`: an S3-compatible `s3:` URL, access key and secret, retention and the default hour, stored in SQLite (credentials sealed). Without one, each server keeps a local restic repository in a `dock_backups_<install>` volume, labelled **not off-box** everywhere it shows. |
| Repository password | Generated once, sealed in SQLite, printed once by `dock backup configure`, shown again to admins with `dock backup status --reveal`. Without it, an off-box repository can't be read after losing the server. |
| Retention | After every backup: `forget --prune` for that service's snapshots (default 7 daily, 4 weekly, 6 monthly), then the snapshot list is read back so Dock's list never shows pruned snapshots. |
| Schedule | Every stateful service, daily at the install's hour (default 03:00, the control plane's local time), overridable per service (`dock backup schedule`). One goroutine and one timer that sleeps until the next due backup. |
| Offline servers | Skipped while offline; when the server reconnects the scheduler wakes and runs what is overdue. |
| Restore | Template services: `dock backup restore <service> --snapshot ID [--as NAME]` creates a **new** service from the template with new credentials and the restored data. The live service is never touched; you switch references when it checks out. |
| Permissions | Read: list backups and status. Deploy: run now, schedule, restore. Admin: configure, reveal the password. |
| Audit and problems | `backup.run`, `backup.succeeded`, `backup.failed`, `backup.restore`, `backup.restored`, `backup.restore_failed`, `backup.configure`, `backup.schedule`. The latest backup of each service is on the overview; a failed one is a problem. |

## Templates

A template is small, typed Go: image, port, mount path, memory, start
command and the variables it generates. It is not a new kind of service:
everything after creation (settings, variables, volumes, deploys, rollbacks)
works as for any other service, and the `template` column only drives
health checks, backups, restores and data setup.

| | Postgres | Redis |
|---|---|---|
| Image | `postgres:17-alpine@sha256:…` | `redis:8-alpine@sha256:…` |
| Volume | `data` at `/var/lib/postgresql/data` | `data` at `/data` |
| Memory | 512 MB | 256 MB |
| Start command | `docker-entrypoint.sh postgres` with `shared_buffers=128MB`, `effective_cache_size=384MB`, `work_mem=4MB`, `maintenance_work_mem=64MB`, `max_connections=50`, `random_page_cost=1.1` | `docker-entrypoint.sh redis-server` with `--requirepass`, `--appendonly yes`, `--maxmemory 200mb`, `--maxmemory-policy noeviction` |
| Variables | `POSTGRES_USER=postgres`, `POSTGRES_PASSWORD` (generated), `POSTGRES_DB=app`, `DATABASE_URL` built from them with references | `REDIS_PASSWORD` (generated), `REDIS_URL` built from it |
| Others use | `${{postgres.DATABASE_URL}}` | `${{redis.REDIS_URL}}` |

- Passwords are 32 random alphanumerics, safe in URLs without escaping.
  They are ordinary service variables (sealed, revealed only to admins).
- The start command keeps the image's entrypoint (`exec
  docker-entrypoint.sh ...`), so the image still initialises the volume.
- `noeviction`: Redis is often a queue; dropping keys silently is worse
  than an error. Change the start command for a cache.
- Redis 8 is AGPLv3 (tri-licensed). Valkey is the BSD alternative and a
  later template.
- Creating a template service deploys it right away (a plain `service
  create` doesn't): a template is complete, and the deploy applies its
  staged variables and volume through the normal path.

## Jobs

`node.Containers.RunJob(ctx, JobSpec) (JobResult, error)`: create a
container with restart policy `no`, the job role label (so it is never
listed, reconciled or reported as an app), `no-new-privileges`, the given
image, env, network, mounts and limits, the script as `/bin/sh -c`; wait
for it to exit; return its exit code and the tail of its output (256 KB);
remove it. Cancelling ctx kills and removes it. Remote servers get it as
the `run-job` op (node protocol minor version 2); an older node answers
with an error that says to upgrade dock there.

Why not exec into the database container: it needs a new exec primitive,
runs backup tools inside the app's cgroup and memory limit, and can't mount
the restic binary. A short-lived container in the same image costs a
second of start-up, once a day.

The restic binary comes from `restic/restic` (pinned by digest, `[backup]
restic_image` in the daemon config): Dock copies it into the server's
`dock_restic_<hash of install and image>` volume once per daemon start, and every job mounts
it read-only at `/dock-restic`. restic is a static binary, so it runs in
any image.

## Repository and settings

The repository, credentials, retention and default hour are install-wide
settings in SQLite (`backup_settings`), set with `dock backup configure`
(admin) rather than the daemon's config file: they change at runtime from
the CLI or UI without a restart, and credentials must be sealed like
variables. Only the restic image lives in `[backup]` in the config file,
next to the BuildKit image.

- `s3:` URLs only for off-box (any S3-compatible store: AWS, R2, B2, MinIO).
  Jobs run on the environment network, so a MinIO on the LAN needs
  `[network] allow_private`.
- Snapshots are tagged `dock-s<service id>` and use host `dock`; retention
  is per tag. Two installs must not share one repository path.
- Changing the repository keeps the old rows, marked with where they went;
  restores read from the repository a backup was written to, as long as the
  credentials still match.

## Scheduling

A service is due when the most recent occurrence of its hour has passed and
it has no scheduled backup attempt since. The scheduler goroutine computes
the earliest due time across services in one query, sleeps on one timer
until then (or until kicked by a schedule change, a configure or a server
reconnecting), runs due backups one at a time, and repeats. A failed
attempt counts as an attempt: it is a problem on the overview, and "Run
now" retries; the next try is the next day. With no stateful services the
timer is not armed at all.

## Restore

| Engine | Steps |
|---|---|
| Postgres | Create the new service from the template, deploy it, wait for the release to go active, then a job runs `restic dump | pg_restore --no-owner --no-acl` into it. Data setup runs as for any template. |
| Redis | Create the new service from the template, create its volume, a job writes the snapshot as the AOF base file with a manifest (Redis with `appendonly yes` ignores a plain `dump.rdb` on first start), then deploy. |

The restore returns the new service at once; progress and the outcome are
audit events and a restore row in the source service's backup list. The new
service has its own credentials; nothing references it until you change
`${{postgres.DATABASE_URL}}` to `${{postgres-restored.DATABASE_URL}}` (or
rename at your own pace).

Generic volume backups are restored by hand for now (`restic restore` with
the repository password); restoring them into a new service is a follow-up.

## Consequences

- One new runtime call (`RunJob`) and one new health check field, both
  implemented locally and over the node link.
- Idle cost: one goroutine and at most one timer, firing at most a few times
  a day. A backup costs one short container and the dump's CPU and IO.
- Backups work with no configuration (local, not off-box) and get off-box
  with one command.
- The repository password lives with the control plane. Losing both the
  server and the printed password loses the backups; `dock backup
  configure` says so.
- The platform's own SQLite file isn't backed up by this yet (follow-up,
  same repository). Done in [0021](0021-control-plane-backup-and-move.md).

## Amendment (2026-10-22): jobs run in the template's image

Decided by the agent after a security review.

Backup and restore jobs carry the install's repository password and S3
credentials: one repository holds every environment's backups. They ran in
the service's *active* image, and anyone with `deploy` on an environment
can deploy any image to a template service. A token limited to
`myapp/staging` could deploy an image whose `/bin/sh` prints its
environment, run a backup, and read (or forget) production's backups.

Jobs for template databases now always run in the template's pinned image
(`dbTemplate.image`), whatever the service runs, and the restore of a
Postgres snapshot no longer reads the new service's image either. The
volume of a generic service is still backed up from the restic image.

- `pg_dump` dumps servers of its own major version and older, so a
  service moved to a newer major (another image) fails its backups with
  `pg_dump`'s version mismatch error until the template catches up.
  Minor versions are fine.
- Job output that ends up in errors (audit, alerts, the backup list) loses
  control characters first.
- Still open: the job's environment, credentials included, shows in
  `docker inspect` while it runs. Only root and the `docker` group can
  read it, and they can read the daemon's secrets key anyway; avoiding it
  needs a way to hand a job a secret file, a new runtime primitive.

## Amendment (2026-10-30): storage connections

Where backups go is a storage connection and a folder in its bucket, not
a repository URL with its own keys, and moving them keeps every backup
restorable ([0039](0039-backups-on-storage-connections.md)).


## Amendment (2026-11-10): query stats

The Postgres template's start command also loads `pg_stat_statements`
(`-c shared_preload_libraries=pg_stat_statements -c
pg_stat_statements.track_utility=off`), and Dock creates the extension when
a template release goes active, for the Data panel's Health tab
([0069](0069-database-insights.md)). Databases created before keep their
command until someone turns query stats on, which stages the change for
their next deploy.
