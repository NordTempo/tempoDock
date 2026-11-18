# 0091: Traffic switches without Caddy reloads

Status: accepted (decided by the agent for the coordinator, 2026-11-17).
Amends [0004](0004-caddy-as-separate-proxy.md) (how routes reach a
container) and [0063](0063-moves-without-downtime-and-server-drains.md) (the hold's port).

## Context

A deploy, restart, config apply or rollback switches a service's traffic
to a new container with a new host port. Routes dialed `127.0.0.1:<port>`
straight from Caddy's config, so every switch was a full config `/load`.
Caddy then shuts the old config's servers down, and Go's
`http.Server.Shutdown` closes every idle keep-alive connection at once. A
client that sends its next request on one of them in that moment gets
`EOF` or a reset. The nightly e2e run (`run/zerodowntime`) lost 1 or 2 of
about 2,000 requests in 1 to 2% of switches. 3 of 32 runs failed on main,
and 1 of 24 on 3c5dc9a8, so the bug is older than both.

Moves had a related problem. Once a container in a move's hold died,
Docker's port proxy still accepted on its port and reset the
connection, and Caddy retries only GETs after a connection was made. So
POSTs got a 502 in those few milliseconds.

Options weighed, with Caddy 2.11.7's source:

| Option | Verdict |
|---|---|
| `grace_period`, `shutdown_delay`, keep-alive settings | No. The grace period only bounds the wait for active connections: idle ones close at once. `shutdown_delay` applies only to listeners that close, not to reloads |
| Admin API patch paths (`PATCH /config/.../upstreams`) | No. Every admin change is a full reload of the config |
| `dynamic_upstreams` `a`/`srv` against a resolver Dock runs | No. `a` has no stale-on-error, so a Dock restart turns into 502s once the cache expires. An `srv` target needs a DNS lookup on every new dial, because Go sends `name.` to DNS and not to `/etc/hosts`, which puts Dock in the dial path. Lookups run under a global write lock, the cache holds 100 entries, and a switch lags by `refresh` |
| A forwarder per service in dock (or iptables DNAT) on a stable port | No. It adds a hop to every byte, and a Dock restart cuts the forwarded connections (against 0004). It also fails to switch: Caddy pools keep-alive connections by the dial address, and a stable address keeps the pooled ones on the old container for as long as traffic keeps them busy |
| The upstream address in a file Caddy reads per request (`{file.*}` placeholder) | **Chosen** |

## Decision

**A service's upstream lives in a file.** A route Dock gives a `Key` (a
service's routes: `s<service id>`) dials `{file.<dir>/<key>}`, and the
file holds `127.0.0.1:<port>`. Caddy reads the file on every request and
again on every retry. A switch writes the new address to a temporary
file and renames it into place, and the next request goes to the new
container. Nothing else in the rendered config depends on the release:

- The analytics tag's release (`site.service.release`) comes from a
  second file, `<key>.release`. The trace token was never in the config
  (`Proxy.TraceToken`).
- Keyed routes always retry a refused connection for 10 s, every
  250 ms. A move's hold therefore changes no config either: its
  requests wait in the control plane behind a port that accepts, for
  up to `HoldFor` (60 s, 0063's `waitHold`), not in Caddy's retry. A restarting
  container is waited out instead of answering 502. A service that
  stays down answers 502 after 10 s instead of at once.

**The retry window,** measured on the e2e servers. These are the times
a keyed route's upstream refuses connections:

| Case | Refused for |
|---|---|
| Stateful redeploy: the old container stops before the new one starts | 0.69-0.78 s (5 runs, a small Go app) |
| An app process crashes, and Docker's restart policy starts it again | 0.57-2.08 s (5 kills; Docker's backoff doubles) |
| The control plane restarts, until its node is linked again (forwards listen again) | 0.93-0.99 s (3 runs) |

10 s covers all of them with a margin of four or more, and also apps
that boot for a few seconds. It is short enough that a dead service
doesn't read as Dock hanging. A switch never refuses at all: the new
container passed its health check before the file changes.

So a switch loads a config that is byte for byte the running one, and
Caddy answers it without reloading. Caddy pools upstream connections by
the address it dialed, so the switch is exact. Requests that started on
the old container finish there, and its idle pooled connections are never
used again. They close when it stops.

**Where.** `[caddy] upstreams_dir`, by default `/var/lib/dock-upstreams`.
install.sh makes it `dock:dock-caddy 2750`, and both dock units may write
it (`ReadWritePaths`). Files are `0640`, readable by Caddy's group. The
files outlive restarts, so Caddy routes with `--resume` after a reboot
whether or not dock is running, as before. A server where the directory
is missing or can't be written keeps static upstreams, and its switches
reload as before. A node that predates this ignores `Key` and does the
same, so no protocol bump.

**Order.** A file a config needs exists before that config loads. A file
the running config already reads changes only after a new config loaded,
so a rejected config never moves traffic. Files of keys that are gone are
removed after the load.

**Moves.** At the hold, the source listens on a fresh port for each
service with domains, and the service's file points there *before* its
container stops. Requests wait in the control plane (0063's `waitHold`),
and none reach a dying container. The fresh port, not the release's,
becomes the forward at the switch. A rollback starts the containers on
their own ports, points the files back, and only then closes the hold's
listeners. Hold ports come from the host port range and are reserved in
memory until the switch records them, inside the write transaction that
allocates release ports, so the two never collide.

**What it costs.** Measured on the dev VM with Caddy 2.11.7 and a
trivial backend at 16 concurrent clients (200k requests): Caddy's CPU
went from 16.3 to 18.3 µs per request (open, read, close of a small
cached file), its p50 stayed at 0.4 ms, and saturated throughput fell
7-13%. Real apps spend far longer than that per request. Dock pays
nothing per request: it writes two small files per switch, and nothing
runs while idle.

## Consequences

- A traffic switch never reloads Caddy, so no keep-alive connection
  is closed under a client. Other config changes (a domain, access,
  analytics or tracing settings, an environment falling asleep or waking
  up, a service stopping or starting) still reload, with the old race.
  They are rare.
- `/reverse_proxy/upstreams` lists a keyed route under its placeholder,
  so `Proxy.OpenRequests` maps addresses to keys. Sleep's open-request
  check counts the service's requests, as before.
- A missing file makes Caddy log an error and answer 502, so dock writes
  files before the config that reads them.

## Amended 2026-11-19: sleeping environments and moves

A move's hold never meets a sleeping environment, so it never has a wake
check to drop. `beginMove` wakes an asleep environment before the move's
rows exist (a move copies what runs, ADR 0070), and `putToSleep` refuses
while a move is open. The wake is an ordinary one: it takes the wake
check off the route and reloads Caddy before the first step, as any wake
does. From the hold to the switch only the upstream file changes
(`TestSleepFollowsMove` covers the order).

The question was whether the wake check should stay on a held route and
answer "moving, retry shortly" (503 with `Retry-After`) instead of
waking mid-move. It isn't needed: there is no request a held route could
receive for an asleep environment, and a request during the hold already
waits for the switch.

`asleep && !held` in `applyRoutesLocked` stays, for forwarded routes
(they count as held). An environment that falls asleep on its new server
must not put a wake check on the old server's forward: the old server
can't answer it (the control plane's check only knows its own server's
environments, a node's ignores an environment that isn't on it), and
adding it would reload the old server.

Known gaps, not built:

- A request that still reaches the old server (a cached DNS answer, a
  domain Dock doesn't manage) while the environment sleeps on its new
  server gets a 502 from the forward instead of waking it. Waking through
  a forward means the wake check accepting a forwarding server.
- An environment without `sleep_after` that is put to sleep by hand on
  its new server turns on `QuietProbes` for the old server's forward at
  that server's next route apply: one reload there. Taking `QuietProbes`
  off forwards would move that reload to every switch, which is worse.
