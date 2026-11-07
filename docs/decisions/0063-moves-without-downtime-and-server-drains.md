# 0063: Moves without downtime, and draining a server

Status: accepted (decided by the agent, on Saile's direction, 2026-11-07).
Amends [0053](0053-moving-environments.md).

## Context

Saile: an operator taking a server into maintenance, or taking a kind off
it, adds a stand-in server, moves everything over and removes the old one.
That has to happen without downtime and without losing a write, including
the writes that land while the move runs, and the platform's users should
not notice.

0053 moves one environment: stateful services stop for the whole copy (a
`tar` stream), generated domains change hostname, requests in the switch
are refused, snapshots and log history stay behind, and deploys during a
move are refused. Removing a server after a move failed on foreign keys
(its move history and snapshot rows). Nothing moves a whole server.

| Force | Means here |
|---|---|
| No write lost | Every write acknowledged before the switch is on the target, every one after it goes to the target |
| No refusal | Requests wait a few seconds, they don't fail; deploys wait, they aren't refused |
| Any volume | Postgres, Redis, SQLite in an app, uploads: one mechanism, no engine plugins |
| Bounded hold | The few seconds requests wait are budgeted; a move that can't keep them fails before anything stops |
| Any pair | Local and remote servers, behind NAT or a tunnel, over the node link only |

## Decision

| Topic | Decision |
|---|---|
| Steps | prepare, **sync**, stop, copy, start, switch, cleanup (0053's, with sync added and stop and copy redefined) |
| Sync | Volumes copy while their services run, in passes: each sends only what differs. Passes repeat until one takes at most 2 s, stops getting quicker (more than 90% of the one before), or six ran. The last one predicts the final copy. A service stopped on purpose gets one exact pass. The environment's snapshots copy here too (one exact pass each) |
| Budget | `max_hold` per move, default 30 s (5 to 600). If the predicted final copies of all services together exceed half of it, the move fails during sync, before anything stopped: "move at a quieter time, or allow a longer hold" |
| The fence | stop: new commands are refused (deploys wait, below), running ones end first (up to 5 min), Caddy holds the environment's requests, and every container of it stops cleanly on the source, stateless services first so no request is in flight when the databases stop. After this nothing can write to the source volumes: the only processes that could are stopped |
| Final copy | copy: one exact pass per volume, then a check pass that compares without changing anything and must find no difference, or the move rolls back |
| Hold | Routes of a held service (and every forwarded one) get `Route.Hold`: Caddy's `try_duration` (60 s) and `try_interval` (250 ms), so a refused connection is tried again instead of answering 502. Dial failures are always retried, POSTs included |
| Forwarding | At the switch each service with domains keeps its old host port on the source as a `server_forwards` row. The source listens there (`node.Proxy.Forward`) once the target serves, and its Caddy keeps routing the service's domains to that port; each connection is piped to the service's active container wherever it is now (`node.Containers.Dial`). The source's held requests go through the forward, and so do the stragglers: DNS caches, records Dock doesn't manage, Cloudflare catching up. Forwards last until the server is removed or the environment moves back to it |
| Copy tool | `dock-sync`, a small static binary dock embeds next to dock-probe and bind-mounts into the two job containers of a pass (`JobSpec.Sync`). `recv` on the target lists its tree, then applies what `send` on the source sends back; the control plane pipes each one's output into the other's input over the existing job streams |
| What differs | Type, size, modification time (ns), mode, owner, link target, device. Hard links stay links. Every file's bytes carry a SHA-256 the receiver checks; a stream cut short fails the pass. Files are written beside their place and renamed in, and the receiver flushes the file system (`syncfs`) before it reports |
| Racy files | In a live pass a file that changed while it was read, or was modified within 2 s of the read (a write in the same clock tick would keep its time), gets time 0 on the target, so the next pass sends it again. The fence makes the final pass exact |
| Deploys | A deploy during a move is accepted and waits ("Waiting for server maintenance") until the move ended, then runs on the server the environment is on, with a host port there. Commands (`dock run`, scheduled jobs) run during prepare and sync, whose copy catches their writes, and are refused only in the hold's few seconds. Backups, snapshots and restores stay refused for the whole move |
| Domains | Generated domains keep their hostname (the source forwards them) and the service gets one under the target's base domain when it differs. The move's notes list the DNS records to change for domains Dock doesn't manage, which keep working through the forward meanwhile |
| Snapshots | Copied in sync, their rows repointed in the switch transaction, the source's copies removed in the clean-up |
| Drains | `dock server drain <server> [--kind]`, `POST /servers/{s}/drain` (with `dry_run`), the console. A plan first: each environment, its target and its data, with every move's pre-flight. Placement picks a target per project and kind (most free memory among online servers that accept the kind, not draining, counting what the plan already put there), or `--to project=server`. A plan with problems doesn't start. Starting stops the server taking the kind (or any placement). One move at a time; a move a deploy, backup or snapshot keeps from starting is tried again every 15 s; a failed move (its environment stays where it was) pauses the drain for `--resume` or `--resume --skip`; `--cancel` stops after the move that runs. Operators only, audited on the platform; `server_drain` events for operators |
| Removing | A server is removed with its forwards, drains, the moves to and from it and its snapshot rows; the audit log keeps the record. Refused while a move to or from it, or its drain, runs |
| Protocol | 21: `run-job`'s `sync`, the routes' `hold`, `forward-listen` and `forward-accept`. A move needs both servers on 21 |

## Why this shape

**A fence, not replication.** Every live migration of files works this way:
copy while running, stop the writer, copy the difference. It is correct for
any program that keeps its data in files, which is all of them, and the
stop is a clean shutdown, not a crash. Postgres streaming replication would
fence in under a second at any size, but it is one engine's answer and
needs a TCP path between servers. The hold budget makes the fence's cost
visible and bounded; replication stays the escape hatch if real databases
don't fit it.

**Our own small copier, not rsync.** rsync would need an image on every
server and a way to drive its protocol over stdio both ways. dock-sync is
about a thousand lines of Go with no dependency beyond x/sys, ships inside
dock like the probe, works
with the job streams the node link has, and adds what rsync's quick check
lacks for live passes: the racy-file rule, a hash per file and a check
pass.

**Hold in Caddy, forward from the source.** The source's Caddy is where
requests arrive during the switch and for a while after it, whatever DNS
says. Holding there turns the stop into latency, and forwarding from the
same port means the held requests need no new route: they retry the same
upstream, which starts answering through the forward. The node link
carries the bytes, so it works from behind NAT and tunnels too.

**One environment at a time.** A drain is maintenance, not a race: one
move's disk, network and failure at a time keeps the blast radius small
and the plan readable.

## Consequences

- A move holds requests for the final copy, a clean stop and a start with
  its health checks: seconds for small databases. WebSockets and SSE
  connections drop once and reconnect.
- A move needs `dock-sync` (`make build`) and protocol 21 on both servers.
- The source keeps forwarding its moved services until it is removed; the
  control plane carries those bytes for remote servers.
- Forwarded connections are capped like other dials (64 per target
  server); Caddy reuses its upstream connections, so that is concurrency,
  not requests.
- Log history of releases before the move stays on the source and goes
  with it when it is removed.
- One migration: `server_drains`, `server_drain_items`, `server_forwards`,
  and the rebuilt `environment_moves` and `environment_move_services`.
