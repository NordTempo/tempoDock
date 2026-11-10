# 0053: Moving an environment to another server

Status: accepted (decided by the agent, on Saile's direction, 2026-11-03).
Amends [0015](0015-server-pools-and-placement.md). Superseded in part by
[0063](0063-moves-without-downtime-and-server-drains.md): volumes copy
while their services run and requests are held for the switch instead of
stateful services being down for the copy; generated domains keep their
hostname; snapshots move along; the source forwards what still reaches
it; deploys during a move wait.

## Context

Placement ([0015](0015-server-pools-and-placement.md)) picks a server for
a project's first environment of a kind, and environments stay there.
0015 left moving them for later: "stateless services are easy; volumes
need a data copy". It is needed now: a new production server joins, the
homelab takes a staging environment off the VPS, a server is retired.

| Force | Means here |
|---|---|
| Safe | Nothing on the source changes until the target serves. A failure before the switch leaves the environment where it was |
| Short downtime | Stateless services move without any; stateful ones are down while their data is copied, and that window is visible |
| Crash-proof | A daemon that dies mid-move resumes or rolls back on start |
| Any pair | Local to remote, remote to local, remote to remote, over the node link ([0014](0014-node-protocol.md)) |
| No DNS at IPs | Dock never points DNS at an IP ([0036](0036-cloudflare-api.md)) |

## Decision

| Topic | Decision |
|---|---|
| Interface | `dock env move <env> --to <server>` (waits, showing each step; `--detach` returns at once), `dock env move <env> --status`; `POST /projects/{p}/environments/{e}/move`, `GET .../move`; the environment's settings in the UI, with live progress. Operators only (servers are the platform's, [0027](0027-organizations.md)), and the environment lock applies like any change to the environment |
| Pre-flight | Refused with the reason, before anything changes, unless: both servers are online and the target is a different server that accepts the environment's kind (being disabled for placement doesn't stop a move, as it doesn't stop a pin); the target has the memory the environment's services reserve free, and the disk for its volumes plus a tenth of the disk; every active release's image can reach the target (in the platform registry, or an image deploy it can pull); a domain Dock routes through Cloudflare has a tunnel on the target; no deploy, backup, snapshot or restore of the environment runs, and no other move |
| Steps | Recorded in `environment_moves` (and one row per service), so a restart knows where it was: **prepare** (pull the images, make the network and volumes, reserve host ports on the target), **stop** (the stateful services stop on the source: the downtime starts), **copy** (each volume streams from the source's Docker to the target's), **start** (every service starts on the target, stateful first, each with its release's health check: the downtime ends when the stateful ones pass), **switch** (one transaction: the environment, its releases' host ports and containers, the project's placement for the kind and Dock's Cloudflare records point at the target; then both servers' routes are applied), **clean up** (the source's containers and network go) |
| Copy | A job on the source archives each volume with `tar` to its standard output; a job on the target empties the volume and unpacks its standard input. Dock streams one into the other through a pipe on the control plane. Both jobs count the volume's entries, and the counts must match. Same image (busybox `tar`) on both sides, so owners, modes and the volume root's 0700 come back as they were; only a symlink's own owner becomes root (busybox doesn't lchown) |
| Downtime | Only stateful services stop before the copy. Stateless services keep serving on the source until the switch, though they can't reach a stopped database meanwhile. The move records when the downtime started and ended, shows it live, and audits it |
| Rollback | Any failure before the switch removes what the move made on the target (containers, its network; volumes stay, Docker volumes are never removed by Dock, and the next move empties them first), starts the stateful services on the source again, and fails the move with the error. The source's data was never written |
| Crash | On start, and when a server reconnects, a move still running is finished: before the switch it rolls back, after it the clean-up resumes. A move waits for the servers it needs to be online |
| Domains | Domains Dock routes through Cloudflare follow by themselves (their CNAME points at the target's tunnel). Generated domains under the source's base domain are replaced by ones under the target's. Other domains need their DNS pointed at the target: the move lists them, with the records, in its result and audit event. Dock doesn't touch DNS it doesn't manage |
| Stopped services | A service stopped on purpose ([0050](0050-stop-start-service.md)) moves stopped: its image, data and a host port go to the target, nothing of it starts there or on a rollback, and `dock service start` starts it on the target. The move stops and starts releases itself (`removeReleaseContainers`, the release's container spec): `dock service stop`/`start` mark a service stopped on purpose, which a move must not |
| Old data | Nothing is deleted. The source's volumes stay on the source after a move, unused, as a last copy, and the source's snapshots of the environment's services stay in its snapshot volume ([0047](0047-volume-snapshots.md)) until retention removes them; a rollback leaves the volumes it made on the target, possibly half copied (a new move empties them first). The move's notes name all of it, with the commands to remove it by hand |
| Placement | The project's placement for the kind follows the moved environment, so its next environments of that kind are created on the target. Other environments of the kind stay where they are until moved: rule 2 of 0015 ("a project stays together per kind") becomes the default, not an invariant |
| Progress | `environment.move` events on the event bus carry the move (step, per service progress, downtime) to the UI and CLI; the audit log has `environment.move`, `environment.moved`, `environment.move_failed` |
| Node link | No protocol change of its own: the jobs stream through `run-job-stream` (protocol 16, from `dock run`, [0048](0048-one-off-commands.md)), the archive as the export's standard output and the import's standard input. A remote server needs protocol 16 |

## Why this shape

**Same releases, new containers.** A move starts the active release of
every service again on the target, from the same image and resolved
config. No new release is created: the history reads as it was, and a
rollback later still has its images (pulled from the registry where
needed).

**Stop, copy, start, rather than sync.** A first copy while the service
runs and a short final sync would cut downtime, but needs rsync on both
ends and a protocol to drive it. A full copy while stopped is simple and
correct; Dock's databases are small. The window is visible, so an
operator moves at a quiet time.

**Switch in one transaction.** Before it, the environment is on the
source in every sense: nothing else in Dock (logs, deploys, backups) sees
the move. After it, the environment is on the target. A crash can never
leave it half-switched, and recovery has only two directions.

**Keep the old volumes.** Removing data is the one step that can't be
undone. Disk on the source is cheap compared to losing the only good copy
because a moved database turns out to be wrong a day later.

## Consequences

- Downtime for stateful services is the copy's duration, plus a start
  and a health check: seconds for a small database on a LAN, minutes for
  gigabytes over the internet.
- Domains with DNS Dock doesn't manage are unreachable until their DNS
  points at the target; tunnel servers and domains from Cloudflare are
  the way to avoid that.
- A move needs both servers online from start to end. A server lost
  mid-move leaves the move waiting for it; nothing is lost.
- One migration (`environment_moves`, `environment_move_services`),
  and the host ports a running move reserves on its target.
