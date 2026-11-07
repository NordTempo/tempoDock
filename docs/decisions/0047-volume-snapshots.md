# 0047: Volume snapshots

Status: accepted (decided by the agent, on Saile's direction, 2026-11-03)

## Context

Backups ([0019](0019-templates-and-backups.md), [0039](0039-backups-on-storage-connections.md))
are restic snapshots, daily, ideally off-box. They are the answer to
losing a server, not to "this migration might go wrong". Before a risky
action people want a copy they can take in seconds and put back in
seconds, on the same server, without a repository, a password or a dump
format.

| Force | Means here |
|---|---|
| Any filesystem | Volumes are plain Docker named volumes, usually on ext4. No LVM, ZFS or btrfs snapshots to lean on |
| Any service | Template databases and every other service with a volume |
| Consistent | A database copied while it writes is useless |
| Short downtime | Snapshots run before deploys and `dock run` on live services |
| Remote servers | The same over the node link ([0014](0014-node-protocol.md)) |
| Disk | Copies live on the server's disk, next to the apps |

## Decision

| Topic | Decision |
|---|---|
| Mechanism | A job container (the restic image, for its busybox) mounts the service's volumes read-only and the server's snapshot volume, and copies each volume with `cp -a` into `/snapshots/<id>/<volume>`. While it runs, the service's running containers are **paused** (cgroup freezer): processes stop between two instructions, connections stay open and wait. The copy is exactly what a power cut at that moment would leave, which Postgres, Redis and any crash-safe store recover from. The container is unpaused when the job ends, whatever the outcome |
| Where | One volume per server, `dock_snapshots_<install hash>`. A snapshot is a directory named after its ID, copied into `<id>.part` and renamed when complete, so a cut-short copy is never taken for one |
| Room | The job measures the volumes and refuses a copy that would leave less than a tenth of the disk free, saying how much it needed |
| Disk | Snapshots are on the server's disk, so its disk alerts count them. The disk breakdown ([0035](0035-server-disk-breakdown.md)) shows them as "volume snapshots" |
| Retention | Per service, the newest `[backup] snapshot_keep` (5) are kept, and none older than `[backup] snapshot_days` (7). Applied after each snapshot, and for age on every backup scheduler pass (at least daily): no timer of its own. Only the newest failed snapshot is kept, for its error |
| Restore | **Template databases restore into a new service** by default, like backup restores: created from the template with the source's image and the credentials the data files hold (user, password, database), references made anew, its volume filled from the snapshot before its first deploy. The source isn't touched. **In place** (`--in-place`, any service): the service is stopped, its current data is snapshotted first (the restore's *safety snapshot*), the snapshot is copied in, and the active release starts again. A failed copy puts the safety snapshot back before starting. While a restore writes a service's volumes (in place, or filling a new service's), every other writer is refused with what runs: deploys, stops and starts, and `dock run` with the volumes open for writing; a restore waits for none of them either, it is refused while one runs |
| Automatic | Before every in-place restore (the safety snapshot). Before the pre-deploy command ([0045](0045-pre-deploy-command.md)) with the service's `pre_deploy_snapshot` setting on (opt-in; also `deploy.pre_deploy_snapshot` in dock.toml): every deployed stateful service in the environment is snapshotted, since the migration may touch any of them, and each snapshot is a release event. Before `dock run` ([0048](0048-one-off-commands.md)) when it mounts the service's volumes for writing (read-only runs can't change them); `--no-snapshot` (`no_snapshot`) skips it, and the run records the snapshot's ID. Both go through `Core.SnapshotBefore`: it waits for the copy (and up to 5 minutes for a backup, snapshot or restore holding the service), and a failure stops the action before it starts, saying how to skip the snapshot. Triggers are recorded (`manual`, `before_restore`, `before_deploy`, `before_run`, `before_move`) |
| One at a time | A snapshot, restore or delete claims the service like backups do: one backup, snapshot or restore per service at a time |
| Crash | A daemon that dies mid-snapshot leaves containers paused: the Docker runtime unpauses Dock's paused containers when it starts (Dock never pauses anything for longer than a job). Snapshots in flight are marked failed. An in-place restore in flight is marked failed and its service stays stopped, with the error naming both ways out: restore the snapshot again, or the safety snapshot to undo. Starting it on half-copied data could do more harm than staying down |
| Node link | Protocol 13 adds `pause` to run-job. A node too old for it gets a clear error instead of copying a live volume |
| Interfaces | `dock volume snapshot [--note]`, `dock volume snapshot list`, `dock volume snapshot restore <id> [--as NAME \| --in-place]`, `dock volume snapshot delete <id>`; the API under `/services/{service}/snapshots`; MCP `list_snapshots`, `create_snapshot`, `restore_snapshot` (into a new service only: in place is for people); the service's Backups tab. Deploy scope for everything but listing |
| Audit | `snapshot.create`, `snapshot.created`, `snapshot.failed`, `snapshot.restore`, `snapshot.restored`, `snapshot.restore_failed`, `snapshot.delete`, `snapshot.pruned` |

## Why this shape

**Pause and copy, not dump.** A dump (pg_dump) is stop-free but only for
Postgres, restores slowly and only into a running server. A paused copy
works for every service, restores by copying files back, and keeps the
database's exact state, extensions and all. The price is the copy's
duration as a pause, which for the small databases Dock hosts is
seconds, and requests wait rather than fail.

**Pause, not stop.** A stop closes every connection, runs the service's
shutdown, and needs a fresh container (and its health check) after. A
paused process doesn't notice. A stop is consistent too, but slower and
louder; in-place restores do stop, because the data is replaced under
the process.

**`cp -a`, not reflinks or tar.** Reflinks only help on btrfs and XFS,
and busybox's `cp` doesn't do them; the volumes Dock sees are mostly on
ext4. A copy into a directory restores with another copy, and the files
can be inspected with any tool.

**On the server, not in the repository.** Snapshots are for minutes and
days, next to the data. Off-box safety is the backups' job.

**New service by default.** Like backup restores: the data can be
checked before anything points at it. In place is one flag away, behind
a confirmation, and itself undoable through the safety snapshot.

## Consequences

- A snapshot costs the volumes' size in disk, at most `snapshot_keep`
  times per service. Large volumes and a small disk get refused copies
  rather than a full disk.
- The pause is visible as latency: a 1 GB volume on an SSD is a few
  seconds.
- Snapshots stay on the server they were taken on. An environment that
  moves to another server ([0048](0048-moving-environments.md)) leaves its old snapshots
  behind, still listed; they are pruned by age.
- Generic services restore in place only; to look at an old copy
  without replacing anything, restore it into a template database or
  take a snapshot first.
