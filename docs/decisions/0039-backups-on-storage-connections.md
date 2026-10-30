# 0039: Backups on storage connections, and moving them

Status: accepted (decided by the agent, on Saile's request; mock B picked
by Saile). Carries out the follow-up of [0034](0034-registry-object-storage.md)
and amends [0019](0019-templates-and-backups.md).

## Context

Backups ([0019](0019-templates-and-backups.md)) took their own S3 setup: a
restic repository URL, keys and a region on the Backups page. Since
[0034](0034-registry-object-storage.md), buckets are storage connections,
defined once on the Storage page; the registry uses one. Saile asked for
backups to work the same way, and for choosing a bucket to move the
existing backups there and clean up where they were. Until now,
changing the repository left the old backups behind: restores refuse a
snapshot that isn't in the current repository.

Prod keeps its backups in the local repository on its one server, the
control plane's own daily backup included: nothing is off-box yet.

| Force | Means here |
|---|---|
| One way to add a bucket | Storage connections, one bucket each (0034) |
| Nothing unrestorable | Every backup restores still can use must keep working, in the console and the CLI |
| Cleanup is destructive | A source is emptied only once its copy is checked |
| restic | `restic copy` gives copies new snapshot IDs, records the original, and uses one set of S3 keys for both repositories |

## Decision

| Topic | Decision |
|---|---|
| Setting | `backup_settings.storage_id` and `storage_path` (the folder in the bucket, default `dock-backups`). NULL keeps local repositories on each server. The repository URL is derived (`s3:<endpoint>/<bucket>/<path>`), so everything that compares a backup's repository with the current one works as before. Bucket addressing reaches restic as `-o s3.bucket-lookup=path\|dns` |
| Old URLs | A repository URL and keys set with `dock backup configure` become a storage connection named after the bucket when the daemon starts; history is relabelled if the URL is written differently. `configure` refuses the URL and key fields from then on, pointing at `dock storage add` and `dock backup move` |
| Move | `dock backup move --storage NAME [--path P]`, `POST /backup/move`, the Backups page's Move backups. In the background: per source repository (each server's local one holding backups restores can use, the control plane's for its own backup, or the current bucket's) a `restic copy` job into the target, on the server that holds it, creating the target with the source's chunking parameters so it deduplicates. Then one `restic check` and snapshot listing of the target, on the control plane |
| Re-pointing | Each copy job also lists its source, and the check lists the target, as one `original`/`id` token per snapshot (`grep -o` of restic's JSON, so thousands fit in a job's capped output). Every succeeded, unpruned backup in a source, and the control plane's latest, is matched to its copy through restic's `original`: a snapshot copied before keeps its first original, so a second move works too. One that a source holds but the target doesn't fails the move with nothing switched. One that no source holds any more was gone before the move: it is marked pruned and counted (`gone`). Otherwise one transaction points each at its copy and switches the setting |
| Cleanup | After the switch, each source is emptied: a job deletes the local repository's files (the volume stays), or the S3 client deletes the old bucket folder; never a bucket's root. A cleanup that fails is reported per source, and the move still succeeded |
| While it runs | Backups, restores and control plane backups started by hand are refused (409); scheduled ones wait, and the move's end wakes the scheduler. Starting a move is refused while any of them runs |
| Bucket to bucket | Only between connections with the same endpoint, keys and addressing: restic copies with one set of keys. In the same bucket, a folder containing the other (or the bucket's root) is refused, since emptying the source deletes its folder |
| Back to local | Not offered: the move only goes into a bucket. Local repositories are where backups start, and moving back would mean splitting a bucket's snapshots per server |
| Page (mock B) | A Storage section with every repository holding backups (local ones per server, the bucket's), their snapshots and size, which one new backups go to, and each source's progress during a move (one audit event per copied source refetches it). Retention and the hour are their own small form |
| Storage page | Connections show "backups" under Used by. One that backups use, or a move copies into, can't be removed or pointed at another endpoint or bucket |

## Consequences

- Backups become off-box with one action, and keep being restorable
  across the move, which `dock backup configure --repository` never did.
- A move costs the backups' size in uploads, once. The local repository's
  space is given back at the end.
- Snapshot IDs of moved backups change; audit history from before the
  move names the old IDs.
- Bucket to bucket across providers isn't possible yet; restic would need
  separate keys for the source.
- `dock backup configure` keeps its deprecated bucket flags only to say
  what to use instead.
