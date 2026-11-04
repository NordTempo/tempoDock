# 0050: Stopping and starting a service

Status: accepted (on Saile's request). Amends
[0014](0014-node-protocol.md) (protocol 17).

## Context

A service had no stopped state. A container could stop or crash, but
that read as a failure: recovery, reconcile and health restarts brought
it back, and alerts fired. The only way to free a service's memory on
purpose was archiving it, which also removes its routes and hides it,
and coming back meant creating it again. Staging copies, preview
environments and a service paused while debugging another all want
"off for now, everything kept".

## Decision

| Topic | Decision |
|---|---|
| State | `services.stopped_at` and `stopped_by` (migration 0052). While `stopped_at` is set the service's status is `stopped`; `stopped` means stopped on purpose only. A container that is gone or not running without a stop reads `none` |
| Stop | `POST .../services/{service}/stop`, `dock service stop`, MCP `stop_service`, the service page's Stop. Deploy scope. Refused while a deploy is in flight (one transaction with the in-flight check, so a deploy and a stop never both go ahead), for an archived service, and for one with no active release. Stopping a stopped service changes nothing, except that it tries again to remove a container a failed stop left |
| What stop keeps | The active release stays active, with its image, resolved config and host port. Volumes, domains, variables and release history are kept. Only the container is removed, after the routes switch, so no request reaches a container that is stopping |
| Domains | A stopped service's hostnames answer `503` with a small page naming the service, `Cache-Control: no-store`. The route carries the stopped service's name, new in node protocol 17, and no analytics script. A node below 17 ignores the field and proxies to the stopped release's port, where nothing listens: it answers `502` |
| Start | `POST .../services/{service}/start`, `dock service start`, MCP `start_service`, the service page's Start. Deploy scope. A new release copying the active one (source `restart`; its first event and the audit log say it is a start), through the normal health-checked path. The service runs again once that release goes live; if it fails, the service stays stopped. A service that isn't stopped is a conflict |
| Pre-deploy command | A start runs none, like any restart: it copies a release that already ran it |
| Deploys | Any release of a stopped service that goes live (deploy, apply, rollback, promote, restart, start) runs it again: cutover clears `stopped_at` in its activation transaction. A failed one leaves it stopped, also for a stateful service, whose old release has no container to bring back |
| Left alone | Recovery after a daemon restart, reconcile and drift, Docker container events and health restarts skip a stopped service. Crash loop and unhealthy alerts resolve when it stops; an analytics traffic drop on a site whose services are all stopped resolves too |
| Backups | The scheduler skips a stopped service: nothing it holds changes |
| Snapshots | A snapshot restored in place into a stopped service ([0047](0047-volume-snapshots.md)) leaves it stopped: the data is there when it starts. A stop is refused while a snapshot is being restored in place, which stops and starts the service itself |
| Logs | Stop removes the container like any other removal, so its output is archived first ([0046](0046-log-history-and-drains.md)); a stopped service's logs are read from that archive. Without one (logs not kept) the error says it is stopped and to start it |
| Locked environments | Stop and start are changes through `serviceFor` with deploy scope, so a locked environment refuses them to tokens like any other change, with an access request (ADR 0044). A human in a UI session can still stop or start |
| UI and CLI | `dock service show`, `dock status` and the service page say who stopped it and when. Lists and the overview show `○ stopped` in a muted tone, not as a problem |

### Why not the alternatives

- **`docker stop` and keep the container**: a stopped container still
  holds its writable layer and is one `docker start` away from running
  an old config. Removing it and starting from the release keeps one
  path for every container: a release, health checked.
- **A new release source `start`**: the releases table only takes the
  sources it always had, and a start is exactly a restart. The event
  and audit log carry the difference.
- **Scaling to zero**: Dock runs one container per service; a replica
  count would be a much larger change for the same result.
- **Archive and unarchive**: archiving drops routes and hides the
  service; a stopped one keeps answering its domains with a clear 503.

## Consequences

- Freeing a service's memory is one command, and undoing it is another,
  with the same health gate as a deploy.
- A deploy to a stopped service starts it: deploying is a request to
  run it.
- The control plane needs protocol 17 on a node for the 503 page; older
  nodes answer 502 until they update.
- Status `stopped` no longer means "the container exited": code reading
  states treats it as intentional, and `none` with an active release as
  the failure.
