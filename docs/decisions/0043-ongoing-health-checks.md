# 0043: Ongoing health checks

Status: accepted (on Saile's request). Amends
[0020](0020-retention-and-alerts.md) (a new alert kind) and
[0014](0014-node-protocol.md) (protocol 13).

## Context

A release's health check ran once, during its deploy. After that a
container had Docker's `unless-stopped` restart policy and nothing else,
so a process that hangs but stays alive (a deadlock, a full thread pool,
an event loop stuck on one request) was never restarted, never alerted
on, and showed as running. On a single server that is the most common
silent outage.

The daemon must not poll ("idle means idle", [performance](../performance.md)),
so probing every container on a timer from the daemon is out. Docker
can run a healthcheck itself, but its probe is a command run inside the
container, and an app's image may have no shell, curl or wget
(distroless, scratch, slim images).

## Decision

| Topic | Decision |
|---|---|
| Who probes | Docker's native healthcheck (`Config.Healthcheck`). Docker runs the probe and reports `health_status` events on the stream core follows already; the daemon runs nothing per probe |
| The probe | `dock-probe` (`cmd/dock-probe`): a 2 MB static Go binary that imports only `net`, `os`, `strconv` and `time`. dock embeds it gzipped (`internal/node/probe`, built by `make build` and the release, 0.9 MB), unpacks it to `<data dir>/probe/dock-probe` on start when it changed, checks it runs there (`dock-probe version`: a `noexec` data dir is caught) and bind-mounts it read-only at `/.dock-probe` in each container with a check. It works in any image, of any architecture the host runs, with no shell |
| What it probes | What the deploy's check does, from inside the container: `GET health_check_path` on the service's port (2xx or 3xx passes), or a TCP connect when there is no path. Template databases get TCP on their port. A service with nothing to probe (a worker without a port) gets no check. It connects to the container's loopback, then to its other addresses if nothing listens there |
| Settings | Per service and in dock.toml's `[deploy]`: interval (default 30 s, `off` / -1 turns it off), probe timeout (5 s), retries (3) and start period (the deploy's health check timeout, 60 s by default). During the start period Docker probes every 2 s, so a restarted app reads healthy within seconds |
| Unhealthy | Docker marks a container unhealthy after `retries` failed probes in a row. Core shows it in the service's status (`health`, also in SSE, `dock status`, the MCP endpoint and the UI), raises an `unhealthy` alert and restarts the container in place: same container, port and volumes, so a stateful service never has two containers on one volume |
| Backoff | Restarts in a row wait 0, 1, 5, 15, then 30 minutes after the previous one, on top of the time the check takes to fail again (2.5 minutes by default). Half an hour of passing checks starts the count over. Restarts held back by the backoff are made on the statistics tick: no new timer |
| Alert | `unhealthy`, one per service, warning, then critical once a restart didn't help. Its message has the probe's last output and the restart plan. It resolves when the check passes again, a new release goes live or the service stops; the channels get the resolution as the recovered notice |
| Counting | Health restarts are `health_restarts` in the status, not `restart_count`, so they don't also raise a crash loop alert |
| Remote servers | Protocol 13 adds `restart` and optional fields: the spec's health, a container's health and output, and `health_status` events. An older node gets no check (it ignores the field) and the control plane never asks it to restart |

### Why not the alternatives

- **Probing from outside the container** (from the daemon or node,
  through the published port) needs a timer in the daemon for every
  container: the daemon would wake every few seconds forever. It also
  can't reach unpublished services without joining their network.
- **TCP only, with the image's own tools**: there are no tools to rely
  on. Even a TCP check needs a shell with `/dev/tcp` or `nc`, and a TCP
  check doesn't catch a hung app, since the kernel accepts connections
  for a process that never reads them.
- **The dock binary itself as the probe**: it would need no extra build
  step, but each probe would cost about 36 MB of memory and 30 ms in the
  app's own cgroup (measured), and a container with a tight memory limit
  could be killed by its own health check. dock-probe costs 4 MB of
  memory for a few milliseconds.

## Not fighting other features

- **The deploy's health gate** stays as it is and decides whether a
  release goes live. Health events of a release that isn't active yet
  don't count; at cutover core reads the new container's health once.
- **Stopped services**: core only restarts the active release's
  container while it runs and is unhealthy, and `Restart` refuses a
  container that isn't running (`ErrNotRunning`). Nothing starts a
  stopped container.
- **Deploys**: no health restart while the service has a deploy in
  flight, or while the daemon removes its container.
- **External uptime checks** see what the public sees; this is the view
  from inside the server. They don't share state.

## Consequences

- A hung app is restarted about 2.5 minutes after it stops answering,
  with the default settings, and its operators are told.
- Docker runs one exec per container every 30 seconds (every 2 seconds
  during a start period): a few milliseconds of CPU each on the host,
  none in the daemon. Containers from before this release get their
  check on their next deploy.
- A TCP check still can't tell a hung app from a working one; set a
  health check path for that.
- A plain `go build` has no embedded probe: such a dock logs a warning
  and runs without ongoing checks. `make build` and releases embed it.
