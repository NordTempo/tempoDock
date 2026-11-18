# 0098: Recovering a lost server in one flow

Status: accepted (top 5 of the 2026-11-18 product review, approved by
Saile; decided by the agent). Builds part 2 of
[0095](0095-restore-volumes-and-recover-a-server.md), which designed the
flow; this records how it is built and what was decided on the way.
Built: fetching the control plane's backup from the bucket, `install.sh
--restore-from-bucket`, recovery mode, and `dock admin recover` with its
plan, run and report. Not built yet: the fill of a plain volume backup
(one call to 0095's `fillFromBackup` once that is on main; until then
such a service fails in the report and says so), rebuilding a release
whose image went with the server's registry.

## Context

The only server is gone. Every step back exists
([0021](0021-control-plane-backup-and-move.md): the control plane's
archive is in the backup repository; [0019](0019-templates-and-backups.md):
so is every stateful service), but the guide had them as a list to run by
hand, starting with a restic container and the repository password, and
the apps came back on empty volumes first.

## Decision

Two commands, because the first runs before there is a Dock and the
second needs one.

| Topic | Decision |
|---|---|
| Fetching the archive | `dock admin fetch-backup -o FILE`, a command of its own, so it can be run alone and tested. It runs restic in the pinned restic image with `docker run`: before the install there is no daemon and no runtime, only Docker. It takes the bucket (endpoint, bucket, folder, keys) and the repository password from `DOCK_RESTORE_*` or asks for them, finds the newest `dock-control-plane` snapshot, streams the archive to a 0600 file and reads it to the end (`cpbackup.Verify`) before the file gets its name. Secrets go in the container's environment, never in arguments |
| The install | `install.sh --restore-from-bucket` runs that, then goes on as `--restore FILE` does. Not with `--node` or `--old-server-becomes-node`: the old server is gone |
| What must be kept outside Dock | The bucket's keys and the repository password. Nothing else: the archive holds the database, `secret.key` and the backup settings, so the restored control plane opens the repository itself |
| Recovery mode | A file, `<data dir>/recovery.json`, written by `dock admin restore --recovery` (what the install runs after a fetch). No migration: the mode must be set before the database is first opened by a daemon, and ends on this server only. While it exists the backup scheduler runs nothing, services' and the control plane's own: an empty volume must never be backed up over a good one, and retention must not remove what the recovery needs. Scheduled jobs ([0054](0054-scheduled-jobs.md)) wait too: they would run on volumes that are not back yet; one that came due runs once when the mode ends. Missing containers are not reported as drift either. Dock deploys nothing at start-up in any mode, so nothing else has to be held back |
| Who may recover | Whoever can read the file: root and the `dock` user. It holds a random token; `dock admin recover` sends it to the daemon's loopback listener (`/internal/recover/...`), which serves those paths only in recovery mode. Shell access to the server is the proof, as for `dock admin reset-2fa`; the loopback listener alone is not (any process on the machine reaches it). No API route, no MCP tool: there is no token to call them with until someone has signed in, and recovery is a person's action |
| Why the daemon does it | The restores are jobs on the runtime and the deploys need routes, the registry and the event bus. A CLI that opened the state itself would be a second daemon |
| The plan | Read-only. Every live service on this server with an applied volume (previews left out, as backups leave them out), its kind (a template database's dump, or its volumes), and its newest backup with its age. A service with none says why: never backed up, or backed up only into a local repository, which went with the server. One query and one restic job, whatever the number of services |
| What the plan trusts | The bucket, not the restored database. The archive only knows the backups taken before it: a manual backup or a scheduled pass after it is in the bucket and not in the database, and retention may have removed what the database still lists. So one job lists the newest snapshot of every tag (`restic snapshots --latest 1 --group-by tags`, without a lock: the server that held one is gone) and the plan takes each service's newest across its scheduled and manual tags ([0095](0095-restore-volumes-and-recover-a-server.md)). Base backups for point-in-time restore are not loaded: a recovery restores the dump |
| Running it | After the plan is shown and agreed to (`--yes` skips the question; without a terminal and without `--yes` nothing runs). The daemon brings back every service of its own server that has an active release, one at a time (a small server never runs two restores at once), environment by environment: template databases, then services with volumes, then the rest. A plain service's volumes and Redis's are filled before the service starts. Postgres and MySQL start alone on an empty volume, with the credentials the restored variables hold, and load their dump before the next service starts; it is the same service, so no reference switches. Each service starts as a new release copying its active one, through the usual health-checked path, and the run waits for it |
| What a run leaves alone | A service with a container already (so a second run only handles what is left), one stopped on purpose, one in an asleep environment. A stateful service with nothing to restore starts on empty volumes and the report says so |
| The report | Per service: restored (with the backup and its age), started, started on empty volumes, already running, skipped, failed with the reason. The CLI prints it as it goes and `--report` shows the latest again. It is kept in memory: each restore is also a row in the service's backup list with its log, and the audit log has the run's start and its counts |
| A failed start | The release shows why. An image that was only in the lost server's registry needs a new deploy; images in a bucket ([0084](0084-registry-storage-gateway.md)) come back with the restored database |
| Ending | `dock admin recover --done` removes the file: the scheduler starts again and drift is reported again. With `--yes` a run that restored everything ends the mode itself |
| The drill | `e2e/recover.sh`, in `e2e/nightly.sh`: a slot with a MinIO bucket, an app with a volume and a Postgres, backups, the control plane's state and the volumes removed, the two commands, then the data is checked |

`install.sh --restore FILE` stays as it is and doesn't enter recovery
mode: it is also how a control plane moves while its apps keep running.
Someone replacing a dead server from an archive in hand can add
`--recovery` to the same effect (not wired into the install yet).

## Consequences

- No idle cost: no ticker, no query outside a recovery. The scheduler
  checks one in-memory flag per pass.
- No migration and no node protocol change. A recovery's restore rows
  carry the trigger `manual` (the column takes `schedule` and `manual`);
  migration 0128 and protocol 35 were reserved and are not used.
- A database's data access roles (the Data panel's) are in the restored
  variables but not in the fresh server until 0095's roles in dumps
  land; Dock sets the panel up again when it finds none.
- A recovered install takes no backups until someone ends recovery mode.
  The console doesn't say so yet; an alert while the mode is on is the
  next thing to add with the run.
- Other servers of a multi-server install are not this flow's subject:
  they reconnect and their volumes never left (0095).
