# 0028: Updating Dock from the console

Status: accepted (decided by the agent, on Saile's direction)

## Context

Upgrading meant downloading a release by hand, checking it against
`SHA256SUMS` and re-running `deploy/install.sh` as root on the control
plane, then on every node ([install](../guide/install.md#upgrading)).
Operators could not tell from the console that a release was out, and
nothing stopped them from skipping the backup the guide asks for.

The owner wants the console to show a newer release with its notes, and an
update action (and its `dock` CLI equivalent, [0026](0026-ui-cli-parity.md))
that verifies the download, backs up, swaps and restarts, rolls back when
the new version doesn't come up, then updates the nodes.

| Force | Means here |
|---|---|
| Privilege | The daemon runs as the unprivileged `dock` user under `ProtectSystem=strict`; installing a binary and units needs root. The daemon must not get broad root for it |
| Integrity | Only bytes that match the release's `SHA256SUMS` are ever installed, on every server |
| Recoverable | Migrations only go forward. A bad release must leave the install as it was: old binary, old units, old database |
| Idle means idle | No background polling for releases ([performance](../performance.md#idle-means-idle)) |
| One install path | `install.sh` already knows how to upgrade (units, `caddy.json`, firewall, registry). A second implementation would drift from it |
| Private repo | Releases need a token today; later the repository is public and needs none |

## Decision

| Topic | Decision |
|---|---|
| Check | `GET /update`: the releases newer than this control plane, with their notes, from the release source. It is checked when an operator opens the console or runs `dock update`, then cached in memory for 15 minutes (one entry). `?refresh=true` checks again now. Nothing runs in the background |
| Source | `[updates] source`: `github.com/NordTempo/tempoDock` by default (GitHub's REST API, the repository's releases, drafts left out), or an absolute path to a directory of releases (`<dir>/<version>/` holding the assets and an optional `notes.md`) for mirrors, air-gapped installs and tests |
| Token | `[updates] token_file`, default `/etc/dock/github-token` (root:dock, 0640). It is used when it exists, for the check and the downloads, and never leaves the control plane. Once the repository is public the file can go. Without it, a private repository answers 404, and the console says where to put a token |
| Action | `POST /update/apply` with an optional version (default: the newest). Operators only. No downgrades. It updates the control plane when it isn't on that version, then every node behind it |
| Verify | The daemon downloads `SHA256SUMS` first, then `dock-linux-<arch>` (for its own arch and every node's) and `dock-install-<version>.tar.gz`, hashing as it streams them to disk under `<data_dir>/update/releases/<version>/`, and refuses any file that doesn't match. The updater checks them again, as root, against the same `SHA256SUMS` after copying them out of the daemon's reach, and checks that the binary says it is that version |
| Privilege | A root oneshot unit, `dock-update.service`, started by a path unit, `dock-update.path`, when the daemon writes `<data_dir>/update/request.json`. The request carries a version and nothing else that the updater acts on: no paths, commands or arguments. The updater runs `/usr/local/bin/dock admin update`, which is the binary root installed and not the staged one |
| Apply | The updater copies the staged release into `/var/lib/dock-update/` (root, 0700) and verifies it there. It saves the current binary, units and `caddy.json`, then (on the control plane) stops `dock` and writes a control plane backup (the `dock admin backup` archive, [0021](0021-control-plane-backup-and-move.md)) as the rollback point. Then it runs the new release's own `install.sh` with `DOCK_BINARY` set to the verified binary and a clean environment, so the units and everything else are refreshed exactly as a manual upgrade would |
| Health | Control plane: the API at `listen` answers `/api/v1/version` with the new version within 90 s. Node: the new `dock node` reached its control plane and said hello (it writes `<data_dir>/update/connected` with its version) within 90 s |
| Rollback | If `install.sh` fails or the health check doesn't pass, the updater stops the daemon, puts back the saved binary, units and `caddy.json`, restores `dock.db` from the backup (the old binary doesn't know the new migrations), starts it and checks it the same way. Changes made while the new version ran are lost; that is at most the health check's 90 s |
| Result | The updater writes `<data_dir>/update/result.json` and tells the daemon on its loopback listener (`POST /internal/update`); a daemon that wasn't up then reads it when it starts. The daemon audits `platform.updated` or `platform.update_failed` and publishes an `update` event |
| Nodes | Node protocol 4 adds the `update` call ([0014](0014-node-protocol.md) section 3 had it as "later"). The control plane streams the node's binary, the install tarball and `SHA256SUMS` over the existing link, with each file's size and SHA-256 in the call. The node checks them while it writes them to its own `<data_dir>/update/` and writes the same request, so its `dock-update` applies them the same way, without a backup (a node has no database). The control plane then waits for the node to reconnect on the new version. A node only ever goes to the control plane's own version, and nodes go one at a time, after the control plane. Offline nodes are skipped; the next `apply` updates them |
| Older nodes | Nodes before protocol 4 can't take the call. The console and `dock update` list them as behind, with the `install.sh` command to run on them |
| Audit | `platform.update_started` (by the operator), `platform.updated` / `platform.update_failed` (by the system, with the versions, how long it took and the error), `server.updated` / `server.update_failed` per node. All are platform events, with no org |
| CLI | `dock update` shows the versions, the newer releases' notes and each server's version. `dock update apply [VERSION]` starts an update and, with `--wait`, follows it until it has finished. `dock admin update` is the updater itself, run as root by the unit |

## Why this shape

**A path unit, not polkit or sudo.** The daemon can't start a unit without
a polkit rule (not on minimal servers) or a sudoers entry (a shell away
from root). A path unit needs neither: the daemon writes one file in its
own data dir, and systemd starts a fixed command. The trigger can't
choose what runs or with which arguments.

**What the boundary protects.** The `dock` daemon is in the `docker`
group, which is root-equivalent on that machine. So the updater can't make
a compromised daemon harmless, and doesn't pretend to. What it does is keep
mistakes from turning into a broken install. A truncated download, the
wrong architecture or version, or a file changed between the check and the
install is caught, because the updater verifies its own copy of the files.
It also keeps the privileged code small and fixed. It opens staged files
without following symlinks, writes its result through a new file and a
rename, and trusts only the version string from the request. It never
trusts paths from the request.

**Run the release's `install.sh`.** Upgrading has always been "re-run
install.sh", and every release's script knows what that release needs:
new units, a moved user, a changed firewall. Running it keeps one upgrade
path for people and for the console. The updater only adds what
`install.sh` lacks: the backup, the health check and the rollback.

**Back up with the daemon stopped.** The backup is the database as of the
moment the old daemon stopped, so restoring it on rollback loses nothing
the old version wrote. Restoring the database every time we roll back is
simpler than working out whether the new version migrated it, and always
correct.

**Nodes over the link.** Nodes hold no credentials ([0014](0014-node-protocol.md)),
so they can't download from a private repository, and they shouldn't need
internet access to update. The control plane already verified the files.
The node checks that what arrived is what was sent, and its updater checks
the files against `SHA256SUMS` like on the control plane.

## Consequences

- The first update to a version with this feature is manual, as before:
  `install.sh` installs `dock-update.path` and `dock-update.service`. From
  then on, updates can run from the console. The console says when the
  updater isn't installed.
- Nodes need one manual update to protocol 4. After that they update over
  their link.
- An update stops the API for about as long as a manual upgrade does
  (a backup of a few MB, then a restart). Apps and routing keep running, as
  before.
- Rolled-back updates lose whatever changed while the new version ran.
  The result says so.
- `/var/lib/dock-update/` holds the last backup (every secret of the
  platform, root 0700), the previous binary and the verified release. Each
  update replaces them.
- Caddy, cloudflared and BuildKit upgrades that `install.sh` performs are
  not rolled back. They are rare, separate from dock's binary, and the
  release notes call them out.
- A release source that is a directory makes air-gapped updates possible:
  copy the release's assets to the control plane and point
  `[updates] source` at them.
