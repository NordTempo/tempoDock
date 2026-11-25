# 0096: Checked backups: a weekly repository check and restore drill

Status: accepted (decided by the agent for the coordinator, 2026-11-18).
Builds on [0019](0019-templates-and-backups.md) (backups and the
scheduler), [0021](0021-control-plane-backups.md) and
[0061](0061-one-restore-model.md) (the restore a drill runs).

## Context

Dock backs up every stateful service daily and alerts when a backup
fails. Nothing says the backups can be read back. A repository can rot
quietly (a bucket lifecycle rule, a bad disk, a pack cut short), and a
dump can be written fine and still not load. The first time anyone finds
out is the day they need it.

| Force | Means here |
|---|---|
| Idle means idle | No new timer. The backup scheduler already wakes once a day ([performance](../performance.md)) |
| Small servers | A check reads data and a drill runs a second database for a moment. Both must be bounded and run when the server is quiet, never beside a backup |
| Restore is thread A's | The drill calls the existing restore (`RestoreBackup`) and changes nothing in it |
| Me and my teams | No knobs nobody turns: one switch, sane fixed numbers |

## Decision

**Once a week, in the install's backup hour, after that pass's backups,
Dock checks every repository and drills every template database.**
Both are on by default; one install-level switch turns them off
(`backup_settings.verify`).

### Repository check

- `restic check --read-data-subset=1G`: the whole structure (index,
  snapshots, trees) and a random 1 GB of pack data, re-hashed. A fixed
  size, not a share: the cost stays the same as a repository grows (at
  most 1 GB of egress a week from a bucket), and a small repository is
  read in full every week.
- One check per repository: a bucket's on the control plane's server,
  each local repository on the server holding it (the servers a backup
  move would copy from, and the control plane's for its own backups).
  An offline server's check waits for the pass after it reconnects.
- Checks run one at a time inside the scheduler's pass, like backups, in
  the restic image with the backup jobs' limits (one core, the job
  memory cap). restic's check takes an exclusive lock, so a manual
  backup meanwhile waits for it (up to ten minutes). Every job first
  removes the repository's stale locks (`restic unlock`): restic never
  ignores one, so a job killed mid-run would fail every check after it.
- Due when the last attempt on that repository started six and a half
  days ago or more, at the first backup hour after that. A repository
  never checked is due at the first backup hour after the daemon
  started: an upgrade doesn't start reading data at once. A failed check
  is tried again the next day, not the next week.

### Restore drill

- For each Postgres, MySQL/MariaDB and Redis template database with a
  restorable snapshot: restore its newest snapshot with the ordinary
  restore into a throwaway service `<name>-drill` in the same
  environment, wait for it, run a sanity query, then delete the service
  and its volume. Volume backups have no drill; the repository check
  covers them.
- Sanity query: Postgres and MySQL count the user tables (zero tables
  from a snapshot of a database that has some fails the drill); Redis
  answers `DBSIZE`. The drill records the count and how long it took.
- One drill at a time, after the repository checks. A snapshot over
  2 GB is skipped and says so (its restore would need that much free
  disk and minutes of CPU on a shared server); `dock backup drill` runs
  it by hand.
- The drill service is an ordinary restored service while it lives: it
  shows in the environment for those seconds. Its restore run is
  deleted with it: the drill's own row says how it went. Nothing references it, it
  gets no domain, and it is never backed up (it is gone before the next
  pass). A drill service left behind by a crash is deleted by the next
  drill of the same database.
- The drill waits for the restore's done signal: the restore sends its
  outcome once, when it finishes, after its row is written. Nothing
  polls and no timer runs. (Until 2026-11-19 it waited on the restore's
  claim being released, then on its audit event with a 2 minute timer,
  since the claim goes before the row is written.) The drill still takes
  the claim next, so no backup of the database runs while it queries and
  removes the copy.

### What is recorded and shown

- `backup_checks`: one row per attempt (kind `repository` or `drill`,
  the server or service, repository, status, error, a one-line detail,
  times). The newest ten per target are kept.
- Backup settings list each repository's last check; a service's
  backups carry its repository's last check and its own last drill. The
  Backups tab says "checked 3 days ago" or the failure. The line and its
  switch are B's part of the tab; restore actions stay A's.
- A failure raises a `backup_failed` alert (warning): key
  `backup-check:<server>:<repository>` for a check, platform-wide like
  the control plane's backup alert; `backup-drill:<service>` for a
  drill, on the service. The next pass of the same check resolves it.
- `dock backup check` runs the checks now and `dock backup drill` one
  service's drill (operators: `BackupsManage`). MCP has no backup status tool
  today; one is not added for this.

## Consequences

- The scheduler's daily pass gains two indexed queries; once a week it
  runs one job per repository and one short-lived database per template
  database. No new timer, goroutine or table scan
  ([performance](../performance.md), the backup scheduler's row).
- A drill needs free disk for one copy of the database for its
  duration. The 2 GB cap bounds that.
- "Checked" means the structure is sound and a sample of the data read
  back, not that every byte did. A full `--read-data` stays a manual
  restic run, documented in the guide.

## Not built

- A drill for volume backups (thread A's volume restore would be the
  path), point-in-time restore drills, and a full read of all data on a
  slower cadence.
