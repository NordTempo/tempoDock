# 0035: Where a server's disk goes

Status: accepted (decided by the agent, on Saile's request; mock A picked
by Saile)

## Context

The server page showed one number per filesystem: 17.3 GB of 37.2 GB used
on prod. After moving the registry to a bucket
([0034](0034-registry-object-storage.md)), Saile asked what was still
taking the space, and freeing it took a root shell (`docker image prune`),
because Dock's own prune ([0020](0020-retention-and-alerts.md)) only runs
once the disk passes `prune_disk_percent`.

| Force | Means here |
|---|---|
| Idle means idle | Docker's disk usage sizes every image and volume (10 s with 651 volumes on the dev VM): never on a timer |
| One prune | What the page calls reclaimable is exactly what a prune removes, with the same filter |
| Shared layers | Images share base layers; summing image sizes overcounts |
| Nodes | Each server reads its own disk; the control plane only groups |
| Parity | The console's breakdown and Prune are `dock server disk` and `dock server prune` too ([0026](0026-ui-cli-parity.md)) |

## Decision

| Topic | Decision |
|---|---|
| Reading | `Containers.DiskUsage` (node protocol 7, `disk-usage`): Docker's `system df -v`, and a walk of dock's data dir. Read when someone asks: `GET /servers/{server}/disk`, the server page (its own query, not one the page waits for, kept a minute), `dock server disk`. Concurrent requests for one server share one read (singleflight). No timer; the last reading is kept (amendment below) |
| Images | Each image's unique bytes (size less shared size), so they add up: running (a container uses it), for rollbacks (Dock made it and a release may need it), reclaimable (what `PruneImages` removes: dangling, Dock's, unused, not kept), not Dock's. Layers no single image owns are one "shared layers" part |
| Volumes | Dock's volumes by service (named with one query of the server's services, stopped ones too), BuildKit's cache volume, the local backup repository (`dock_backups_*`), Dock's other volumes; volumes Dock didn't make, and another install's on a shared Docker host, go to Other |
| Groups | App data (service volumes, container layers), Images kept, Reclaimable, Build cache, Dock (data dir, local backups, the registry's dir while its images are on disk), Other (volumes not Dock's and the rest of the filesystem, so the parts add up to "used") |
| Prune | `POST /servers/{server}/prune`, `dock server prune <name>`, the section's Prune button: the full-disk prune of 0020 run now (images as above, build cache above `build_cache_keep_mb` on a builder). It runs to the end if the caller goes away (10 minutes at most), and a partly failed prune answers with what it freed and the error. Refused while that server is being pruned. Audited as `server.prune` with what it freed |
| UI | Mock A: one bar split by group, a legend, and a table whose groups open to their parts, with a Reclaimable column whose sum is the Prune button's amount |

## Consequences

- Reading the disk costs Docker a walk of every image layer and volume,
  each time someone opens a server's page (at most once a minute per
  open page). On a server with very large volumes that is noticeable I/O
  for seconds; nothing reads it otherwise.
- Nodes older than protocol 7 can't report their disk; the page says to
  upgrade dock on them.
- Image numbers are Docker's. With the containerd image store, Docker
  reports unpacked sizes, so "images" can be larger than the registry's
  compressed numbers for the same images.
- "Rest of the filesystem" is what is used minus everything counted:
  container logs, the system, and anything else on that filesystem. If
  dock's data dir is on another filesystem, neither it nor the registry's
  storage dir is shown.
- A prune never touches running images, images a rollback may need,
  volumes or backups; images other work on the server pulled are never
  counted as reclaimable.

## Amendment (2026-10-30): the last reading answers at once

Waiting seconds for Docker on every visit was the wrong trade (Saile).
The daemon keeps each server's last reading in memory (one per server).
`GET /servers/{server}/disk` answers with it at once; when it is older
than a minute, the answer is marked `refreshing` and the server is read
again in the background, and the new reading goes out as a `disk` event
(operators only) that the server page patches its query from. Only the
first read after the daemon starts waits. A prune reads again the same
way, so the page shows what it freed. A failed refresh sends the last
reading with its `error`. Still no timer: a read only follows someone
asking, so a server nobody looks at is never read.

