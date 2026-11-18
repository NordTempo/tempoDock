# 0095: Restoring volume backups from Dock

Status: accepted (top 5 of the 2026-11-18 product review, approved by
Saile). Built: the in-place restore (core, API, CLI, Backups tab), manual
backup retention, Postgres roles in backups, restore targets, a volume
backup into a copy.

## Context

Every stateful service is backed up daily ([0019](0019-templates-and-backups.md)),
but only template databases restored from Dock. A service that keeps its
state in a plain volume, which is most of the app templates
([0083](0083-app-templates.md)), was told to restore by hand with restic
and the repository password.

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
- In place must be asked for (`--in-place`); without it or `--as` the
  answer names both ways. The CLI asks for confirmation.
- **In place stays a person's action.** No MCP tool restores a volume
  backup, as none restores a snapshot in place.
- **Into a copy** (`--as NAME`): a new service cloned from
  the source (its settings and variables, no domains), its volumes filled
  from the backup before its first deploy. It is the way to look at old
  data without stopping anything, and never what a recovery uses. The
  copy runs the image of the source's active release, deployed as a
  promotion of it: nothing is built, and a source without an active
  release is refused. Its own variables are copied, literal values too
  (same environment, same readers); references are kept as they are, one
  to the source itself too. A copy whose restore fails is never
  started and is archived under another name, like every failed restore
  into a new service (part 3).
- The restore is a row in the service's backup list, `restore` with the
  service itself or the copy as its target, with its log.

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
- **Postgres roles** (built): a backup saves `pg_dumpall
  --roles-only --no-role-passwords` in the same snapshot stream, ahead of
  the dump, behind a one-line header with its length (`DOCKPG1
  <length>`); a dump without the header is an older one and restores as
  before. A restore creates the roles that are missing, without
  passwords, then runs pg_restore.
- **A restore target is unusable until its restore succeeds** (built):
  `service switch-references --to` refuses a service whose restore is
  running or failed, saying which, in a dry run too. Whether a service is
  such a target is read from the restore row that names it, never stored
  on the service. A failed restore into a new service archives its target
  as `<name>-failed-<restore id>` (`-failed-s<id>` for a snapshot
  restore), which frees the name for the next try and stops its
  container; its volume is kept for a look. It raises a `backup_failed`
  alert on the service restored from, which the next successful restore
  of it resolves.
- A restart during an in-place restore fails it with what to do: restore
  in place again, or restore the safety snapshot.

- A restart during a restore into a new service ends it like any failed
  one (built, 2026-11-19): recovery reads the running ones with one
  query before it marks them failed, and once the daemon has started
  (alerts loaded, routes applied) each target is archived as
  `<name>-failed-<id>` and the alert opens. Nothing polls.
- The backup list says what an in-place restore touches (built,
  2026-11-19). `volumes` names the service's volumes now: a backup's row
  doesn't record which volumes it holds, and asking restic per list
  request is not worth a job, so the confirm names the volumes the
  restore would replace (one added since the backup is left, as above).
  A succeeded in-place restore's run carries `safety_snapshot`, the
  snapshot taken before it. The backups table has no column for it, so
  it is found: the newest `before_restore` snapshot with the restore's
  note, started after the run before the restore and not after the
  restore. The claim on the service keeps other runs out of that window.
  One indexed query, only when the list holds such a restore; a snapshot
  that was removed is not linked.

## Consequences

- No idle cost. One job container while a restore runs, capped like
  backup jobs.
- No node protocol change: the restore is an ordinary job. One
  migration: an index on `backups.restored_to`, to find the restore that
  made a service.
- Manual snapshots taken before this change carry the daily tag and age
  out with the daily ones.
- `restic snapshots --tag dock-s<id>` by hand no longer lists manual
  backups; the guide names both tags.
