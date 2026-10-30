# 0021: Control plane backup, restore and move

Status: accepted (decided by the agent, M3)

## Context

The control plane's state is `/var/lib/dock` (`dock.db` in WAL mode,
`secret.key`, `link.key`, `registry.key`, build logs, uploads),
`/etc/dock/config.toml` and the registry's storage. Until now there was no
supported way to back it up or move it: the guide said to stop the daemon
and tar the directory. M3 ends with moving the control plane from the
homelab to the Hetzner server while the homelab joins as a node, and
[0019](0019-templates-and-backups.md) left the platform's own database as
a follow-up of its restic backups.

| Force | Means here |
|---|---|
| Consistency | Never copy a live WAL database. One snapshot, as of one moment |
| Secrets | `secret.key` opens every sealed value; `link.key` is what nodes trust. Whatever holds them is owner-only |
| Identity | A restored control plane must be the same one: same link key (nodes pin it), install ID (every container's label), secrets key and registry key, so no node re-joins and no app is redeployed for it |
| One control plane | Two control planes driving the same nodes or the same Docker would undo each other's work |
| Idle means idle | No new periodic work beyond one daily backup on the existing scheduler |

## Decision summary

| Topic | Decision |
|---|---|
| Archive | One tar, gzipped as a file: `manifest.json` first, `config.toml` second, then `data/` (the database as a `VACUUM INTO` snapshot, the keys, build logs) and, on request, `registry/`. Left out: the live database and its WAL, `uploads/`, `builds/work/`, Railpack's cache, dot files, a node's own key and link. Files 0600 and directories 0700, inside the archive and wherever it is written or unpacked (`internal/cpbackup`) |
| Snapshot | SQLite's `VACUUM INTO` from a connection that runs no migrations: one read transaction, so writers go on and the copy is consistent; checked with `quick_check` before it is used |
| `dock admin backup` | On the control plane, as root or the dock user, while dock runs. `-o FILE` (default `./dock-backup-<host>-<time>.tar.gz`), created 0600 as `FILE.partial` and renamed when complete, never over an existing file; `-o -` streams it (refusing a terminal). It says what the archive holds |
| Registry | Left out by default: images can be built or pushed again, and the storage can be many GB. `--include-registry` (root) adds it, so rollbacks and promotions of old releases work right after a move. Not needed with the images in a bucket (amendment below) |
| Scheduled | Daily at the install's hour, into the 0019 repository with its retention, as the restic snapshot tagged `dock-control-plane` holding `/dock-control-plane.tar`. The daemon streams the archive into the job's stdin; only the SQLite snapshot touches the disk. Never the registry. Failure: audit, alert `backup:control-plane`, `dock backup status` |
| Restore | `install.sh --restore ARCHIVE` on a fresh server installs everything and runs `dock admin restore`, which unpacks into an empty data dir, checks the database, gives the files to `dock` (and `dock-registry`), then dock starts with the same identity |
| Two shapes | Default: this server **replaces** the old one; environments placed on the old server are here now, to redeploy. `--old-server-becomes-node`: the old server **keeps its apps** and joins as a node under its name |
| Config | The old `config.toml` is kept, except the keys about the machine (addresses, public IP, ingress, BuildKit size, app network, registry listen and storage), which are set as for a new install. Base domain and plain HTTP stay only when replacing. The old file is kept as `/etc/dock/config.from-backup.toml` |
| Nodes | `sudo dock node set-control-plane host:port` rewrites the address in `node.toml`; the running node uses it at its next attempt (at most 30 s), no restart, no re-join: the pinned key is the same. `[node] control_plane` in the config still overrides |
| One control plane | `dock serve` holds an flock on the data dir. `dock admin backup --move` needs the daemon stopped (the lock), writes the archive, then writes `MOVED`: `dock serve` exits 9 there from then on and the unit doesn't restart on 9. `install.sh --node` turns only such a retired control plane into a node |
| Migration | `0021_control_plane_backup.sql`: one row (`id = 1`) with the latest control plane run and the latest scheduled attempt |

## The archive

`dock admin backup` and the scheduler write the same archive
(`cpbackup.Write`). The manifest names the dock version, the schema version
(`PRAGMA user_version`), the install ID, the host, when, the link key's
fingerprint and whether the registry is in it. A restore refuses an archive
whose schema is newer than the binary's migrations ("install dock vX or
newer"), and an older one is migrated forward on first start as usual.

Everything in the data dir is archived except a short deny-list of
transient files, rather than an allow-list of known files: a key added to
the data dir later is then backed up without anyone remembering to, and a
stray file costs a few bytes. Files that vanish while the archive is
written (build logs retention removed) are skipped; files that grow are
archived as they were when reached, which for append-only logs is a
consistent prefix.

Gzip only for files: restic compresses and deduplicates itself, and a
gzipped stream would defeat its deduplication between days.

## Scheduled backups reuse 0019

It fits cleanly: the scheduler already has one goroutine, one timer and
the repository settings, retention and restic volume per server. The
control plane is one more candidate per pass, always on the local server,
due daily at the install's hour counted from its latest scheduled attempt
(or the local server's creation). Manual runs (`dock backup run
--control-plane`, admin) don't move the schedule.

The one new runtime capability is input for a job: `node.JobSpec.Stdin`.
Docker's runtime attaches to the job before starting it, copies the input
and closes stdin at its end. If reading the input fails, the job is killed
before stdin closes, so restic never sees an end of input and never
stores a cut-short archive as a snapshot; the run fails with the reason.
The node link doesn't carry it (the control plane backup only ever runs on
the control plane's own server), and says so.

The repository password protects the archive inside the repository as it
protects the databases (AES-256 in restic). The password itself is sealed
in the database with `secret.key`, so it is also printed once by `dock
backup configure` and shown with `dock backup status --reveal`: keep it
outside the platform, or an off-box backup can't be read after losing the
server. Restoring from the repository is `restic dump` of the latest
`dock-control-plane` snapshot into a file, then the file path below.

Cost: once a day, a `VACUUM INTO` of the database and one restic job; the
timer that was unarmed with no stateful services is now armed once a day.

## Restore and the two shapes of a move

**Replace** (default) is the disaster case and the "bigger server" case:
the old server is gone or retired with everything on it. The local server
row stays the local server, so its environments are expected on the new
machine. Their containers aren't there: on start, reconcile reports each
active release's missing container as drift, and they are redeployed
(`dock deploy`, or the UI's Deploy); stateful services are restored from
their off-box backups into new services (0019). Domains' DNS moves to the
new public IP, or keeps pointing at the same Cloudflare Tunnel (run the
same tunnel token on the new server and stop `cloudflared` on the old one,
or Cloudflare balances between both connectors).

**Old server becomes a node** is M3's plan: the homelab hands the control
plane to Hetzner and keeps running staging. `AfterRestore` turns the old
local server row into a remote server waiting to re-join (no key,
revoked, so `dock server add <name>` re-joins it under its name with its
environments, as for any revoked server); the next start registers the
new machine as the local server (`<host>-2` if the name is taken). On the
old machine, `install.sh --node` with the join token retires `dock.service`,
removes the `dock-registry` container (the node serves the registry on the
same port through the link), moves the old data dir aside to
`/var/lib/dock-retired-<time>` (kept until the move checks out; it holds
secrets) and joins. Its containers carry the same install ID and the same
release IDs, so the new control plane's reconcile adopts them; nothing
restarts.

A restore drops the control plane backup run that was taking the snapshot
(it never finished there) and records `control_plane.restore` in the
audit log.

## Nodes and the new address

Nodes dial; they can only learn a new address from someone. The options:

| Option | Verdict |
|---|---|
| **`dock node set-control-plane` on each node** | Chosen. One command per node over SSH, no protocol change, and works when the old control plane is already gone |
| Old control plane pushes the new address over the link before retiring | Needs the new address known and every node online at that moment, a protocol minor bump, and the old daemon running while `--move` needs it stopped |
| A stable DNS name for the node port | Already possible (join tokens carry whatever `[node] listen` says); documented as the way to never need either |

The running node reads the addresses in `node.toml` again before each
connection attempt (one small file, only when dialing), so the command
takes effect within the 30 s backoff. `node.toml` keeps its owner when
root rewrites it.

## Only one control plane

The pinned key can't tell the old control plane from the restored one:
both hold the same key. What keeps them apart:

- Nodes dial exactly one address, and `set-control-plane` replaces it.
  Tailscale addresses don't move between machines, so a node pointed at
  the new control plane can't reach the old one.
- `dock admin backup --move` takes the data dir's lock, so the archive is
  the last state, and leaves `MOVED`: `dock serve` refuses to start there
  (exit 9, `RestartPreventExitStatus=9` in the unit). Deleting the file is
  the documented way to undo a move that was never restored.
- `install.sh --node` refuses a control plane unless it is retired, and
  then removes `dock.service`, so one machine never runs both daemons
  against its Docker.

Not built: a generation number nodes remember to refuse an older control
plane (fencing). It would only matter if the old control plane came back
at an address nodes still dial, which the steps above rule out; it needs a
protocol change. A server that died and is restored elsewhere without
`--move` should be wiped before it comes back; the guide says so.

## Consequences

- The control plane is backed up daily with no setup, off-box once a
  repository is configured, and a file is one command away.
- A move is: `--move` on the old server, `install.sh --restore` on the new
  one, `set-control-plane` on the nodes, DNS where it pointed at the old
  server. Nodes never re-join; the old server can stay as a node with its
  apps running throughout.
- Archives hold every secret. They are written 0600 and every command that
  writes one says so; storing them encrypted is the operator's job.
- Without `--include-registry`, rollbacks and promotions of releases built
  before the move need their images built or pushed again, unless the
  registry keeps them in a bucket ([0034](0034-registry-object-storage.md)).
- Certificates (`/var/lib/dock-caddy`) aren't moved; Caddy issues new ones
  on demand.
- One new migration (0021), one optional runtime field (`JobSpec.Stdin`),
  one exit code (9, `moved`), one CLI exit name.

## Amendment (2026-10-30): registry in a bucket

With the registry's images in an S3-compatible bucket
([0034](0034-registry-object-storage.md)), `--include-registry` isn't
needed: the restored database names the bucket, `install.sh --restore`
writes the new registry's config for it, and rollbacks and promotions of
old releases work from the first minute. The flag then archives only the
(empty) storage dir. The old server's registry must not run on the same
bucket once the new one does: a move retires it, and
`--old-server-becomes-node` removes its container.
