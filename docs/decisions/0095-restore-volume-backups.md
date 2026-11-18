# 0095: Restoring volume backups from Dock

Status: accepted (top 5 of the 2026-11-18 product review, approved by
Saile). Built so far: part 1's in-place restore (core, API, CLI, MCP
refusal) and part 3's manual backup retention. Not built yet: the Backups
tab action, a volume backup into a copy, the rest of part 3.

## Context

Every stateful service is backed up daily ([0019](0019-templates-and-backups.md)),
but only template databases restored from Dock. A service that keeps its
state in a plain volume, which is most of the app templates
([0083](0083-app-templates.md)), was told to restore by hand with restic
and the repository password. And when the only server is lost, the guide
is a list of manual steps that nobody has run in order: fetch the control
plane archive ([0021](0021-control-plane-backup-and-move.md)) with a restic
container, install with `--restore`, restore each database into a new
service, switch references, restore other volumes by hand. Apps come back
on empty volumes first.

## Decision

### 1. A volume backup restores from Dock

`dock backup restore <service> --snapshot ID --in-place`, the same call
in the API (`in_place: true`), and a Restore action on each row of the
Backups tab.

- **In place** is the same model as an in-place snapshot restore
  ([0047](0047-volume-snapshots.md), [0061](0061-one-restore-model.md)):
  the service stops, a safety snapshot keeps what its volumes hold, the
  job fills them, the active release starts again. A failed fill puts the
  safety snapshot back, so a failure leaves the service as it was.
  Restoring the safety snapshot undoes the restore.
- The job runs restic in the restic image with the service's volumes
  mounted. It restores exactly the volumes the backup holds: each is
  emptied, then filled. A volume added after the backup is left alone and
  the log says so. A volume the backup holds that the service no longer
  has stops the restore before anything is touched.
- In place must be asked for (`--in-place`); without it the answer says
  that this is the only way such a service restores, as snapshot restores
  do. The CLI asks for confirmation.
- **In place stays a person's action.** MCP refuses it and names the
  command to hand to a person, as for snapshots.
- **Into a copy** (`--as NAME`, not built yet): a new service cloned from
  the source (its settings and variables, no domains), its volumes filled
  from the backup before its first deploy. It is the way to look at old
  data without stopping anything, and never what a recovery uses.
- The restore is a row in the service's backup list, `restore` with the
  service itself as its target, with its log.

Template databases keep restoring into a new service from their dump.

### 2. Recovering a lost server

Its own decision: ADR 0098. It builds on part 1: a recovery fills each
service's empty volumes with the same job (`fillFromBackup`), without a
safety snapshot, before the service first starts.

### 3. Restore fixes decided after the 2026-11-18 bug hunt

- **Manual backups have their own retention** (built). A manual backup is
  tagged `dock-s<id>-manual` instead of `dock-s<id>`; the last 5 are
  kept, apart from the daily, weekly and monthly ones. Before, the daily
  rule removed an earlier backup of the same day.
- **Postgres roles** (not built): a backup saves `pg_dumpall
  --roles-only --no-role-passwords` in the same snapshot stream, ahead of
  the dump, behind a one-line header with its length; a dump without the
  header is an older one and restores as before. A restore creates the
  roles that are missing, then runs pg_restore.
- **A restore target is unusable until its restore succeeds** (not
  built): `service switch-references --to` and the UI refuse a service
  whose restore is running or failed, saying which. A failed restore
  marks its target failed, raises an alert, and frees the name by
  archiving the target under `<name>-failed-<restore id>`.

## Consequences

- No idle cost. One job container while a restore runs, capped like
  backup jobs.
- No migration and no node protocol change for part 1: the job is an
  ordinary job. Migration 0125 and protocol 34 stay reserved for the
  failed-target mark.
- Manual snapshots taken before this change carry the daily tag and age
  out with the daily ones.
- `restic snapshots --tag dock-s<id>` by hand no longer lists manual
  backups; the guide names both tags.
