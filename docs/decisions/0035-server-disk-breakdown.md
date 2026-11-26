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


## Amendment 2 (2026-11-26): prune everything nothing needs, or one part

Prod's disk filled to 98% with its registry in a bucket and the spool
empty, so pushes were refused, and nothing in the console could free it:
the prune freed 22 MB. What held the space, and why the prune missed it:

| On prod | Size | Why it stayed |
|---|---|---|
| Images of archived services (about 25 closed previews) | part of 7.4 GB "for rollbacks" | old images go only after a deploy of the same service, which an archived one never gets; the prune took dangling images only, and these are tagged |
| Images from other registries out of the window (five `dock-demo` versions in a day), old template database digests | part of 5.5 GB "not Dock's" | the prune only knew images Dock built or pulled from its registry |
| A cut-off download of the vulnerability database | about 3.5 GB of 6.9 GB "other volumes" | Grype leaves `grype-db-download*` beside the database when its update is killed (a deploy preempting the scanner, the memory limit, a restart) and never removes it; reproduced on the dev VM: 3.4 GB database plus 2.6 GB left over |

| Topic | Decision |
|---|---|
| Targets | A prune runs targets (`apitypes.PruneTarget`). Safe ones, run without targets, by Dock on a full disk and from the console's Prune all: `images`, `scanner` (the leftovers), `cache` (above `build_cache_keep_mb`). Ones that cost something later, only by name: `rollbacks`, `all-cache` (the next builds start cold), `scan-db` (the whole database, downloaded again before the next scan). `POST /servers/{server}/prune` takes `{"targets": [...]}`; `dock server prune NAME --only ...` |
| Images nothing needs | From every release on the server (one query): each live service's window and active and in-flight releases are kept; every other image a release used is unneeded, from any registry; Dock's own images (built, or from its registry) not kept go, tagged or not; other versions of the images Dock pins (the scanners, restic, the registry, the analytics base, and on the control plane's own server BuildKit and cloudflared) go. Never one a container uses, stopped ones too, nor one made in the last hour (a build may not have recorded it yet) |
| Shared hosts | Nothing is forced, so Docker keeps an image any container uses, a stopped one or one created since the listing. Dock's own images (built, or from its registry) go name by name, each only while it still names the picked image; any other image goes by ID, which Docker refuses while another repository names it too. `[retention] keep_images` lists references no prune removes, for what other work uses without always running it (the core integration harness lists the test images it shares the host with) |
| Older rollbacks | With the registry, kept images it has too, other than each service's active release and the one before it (where a plain rollback goes): a rollback further back pulls them by digest, as a release on another server does. Not automatic: during a bucket outage that rollback would fail |
| Scanner | Grype's image has no shell, so a job in the restic image (busybox `du`, `rm`) sizes and removes the leftovers, and runs before every database update and import so they don't build up again. `scan-db` removes the volume. Either stops the scanner first (`scanBusy`), which cancels a running update |
| Archives | Archiving runs the images target on the servers involved once the containers have stopped, so archived services' images go at once |
| Reading | The disk reading sizes each target with the same rules (node protocol 34) and names Dock's volumes: vulnerability database, analytics store, trace store, backup tool. Reclaimable is the safe targets' sum |
| UI | Mock C (Saile): the breakdown stays a reading; under it a Prune list by what each does, Prune all for the safe ones and a Prune per row, then "Not in Prune all: each costs something later" |

Nodes older than protocol 34 ignore the new request fields and prune as
before (dangling images only); freeing their scanner volume says to
upgrade dock on them.
