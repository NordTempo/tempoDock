# 0052: Keeping the anonymous volumes of containers that served

Status: accepted. Amends [0014](0014-node-protocol.md) (an optional
field on remove and on containers, no protocol bump).

## Context

An image can declare `VOLUME` paths, like `postgres:17` at
`/var/lib/postgresql/data`. When no Dock volume is mounted at exactly
that path, Docker gives every container a fresh anonymous volume there,
and that is where the service's data lives. Each new release's container
starts with it empty.

Since v0.19.0 (#79) the runtime removed containers with
`RemoveVolumes: true`, to stop anonymous volumes piling up on the disk.
For the container of a release that went live, that deleted the
service's data for good on its next redeploy, restart, variable change,
rollback, stop, in-place restore, archive, or crash recovery. Before
#79 the data was just as invisible to the next container, but it stayed
on disk and could be recovered by hand.

## Decision

| Topic | Decision |
|---|---|
| Removal | `Runtime.Remove` takes `RemoveOptions{KeepAnonymousVolumes}`. Core keeps them for every container of a release that went live (active or superseded): cutover, stop-first deploys, stop, restore in place, archive, and recovery. A release that never went live (a failed deploy) is removed with its anonymous volumes, as are jobs, `dock run`, a failed start and infrastructure containers |
| One kept copy | `Start` mounts the anonymous volumes for an image's uncovered `VOLUME`s itself, with no source (still anonymous to Docker, and still filled from the image) but labelled with the container's labels and `dock.anonymous=true`. A keeping `Remove` then removes the service's other labelled anonymous volumes that no container uses. So a service keeps at most the copy of the last container it replaced, next to the ones in use. Docker refuses to remove a volume in use, so a running container's is never touched |
| Older containers | Anonymous volumes of containers created before this carry no labels. They are kept and never removed by Dock, as before #79 |
| Warning | A deploy whose container has uncovered `VOLUME`s says so on the release: no volume covers the path, every new release starts with it empty, and only the last replaced container's copy is kept. Add a volume at that path to keep the data |
| Node link | The remove request gets `drop_anonymous_volumes` (keeping is the default, so a request from an older control plane never deletes data). Containers get `anonymous_volumes`. A node older than this ignores the field and removes them, as it has since v0.19.0: update nodes with the control plane |

### Why not the alternatives

- **Revert #79**: one kept volume per replaced container again, without a
  bound, for every image with a `VOLUME`.
- **Carry the anonymous volume over to the next container**, as Compose
  does: it fixes the data starting empty, but only works when the old
  container is gone first. Services without Dock volumes deploy with
  zero-downtime overlap, and two Postgres processes on one data directory
  corrupt it. It needs a stop-first decision from the image's
  `VOLUME`s; a possible follow-up.
- **Creating a Dock volume for every uncovered `VOLUME`**: many images
  declare them for caches. That is a product decision, not a fix.

## Consequences

- No Dock action deletes the data of a container that served. The last
  replaced copy stays on the server until the service's next container
  that served is removed; Docker's own `docker volume prune` can still
  remove it.
- The disk holds at most one extra anonymous copy per such service,
  shown under the service in the disk view.
- Start inspects the image once more per container.
