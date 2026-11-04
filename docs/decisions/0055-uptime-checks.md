# 0052: Uptime checks from another server

Status: accepted (decided by the agent)

## Context

Dock notices a crashed container, a failed deploy and an offline server
(ADR 0020), but not a site that answers 502 while its container runs,
or a domain whose DNS or tunnel broke. The closest alerts are Analytics'
`traffic_drop`, an hour late, and `server_offline`. An operator wants to
know "production is down" within a minute or two, judged from outside
the server that serves it.

| Force | Means here |
|---|---|
| Idle means idle | Checks are a timer. One scheduler with one timer to the next due check, armed only while checks exist ([performance](../performance.md)) |
| From outside | A server can't tell its own public reachability. The probe runs on another server when there is one |
| Bounded history | Downsampled like statistics: a fixed number of rows per check |
| One way to say it | Down and recovered are alerts, through the existing channels |

## Decision summary

| Topic | Decision |
|---|---|
| What is checked | An HTTP(S) URL on one of the service's domains (any path), `GET`, redirects not followed. Up means the expected status (default 200; `2xx` and `3xx` classes allowed) within the timeout |
| Settings | Interval 30 s to 1 h (default 60 s), timeout 1 to 30 s and below the interval (default 10 s), expected status. At most 10 checks per service |
| Who | Project admins add and remove checks; readers see results |
| Where it runs | On an online server other than the environment's, chosen by the check's ID among the online candidates so checks spread out; the control plane when there is no other server. The control plane's own probes run in process |
| Runtime interface | `Probe(ProbeRequest) ProbeResult` over the node link as `probe` (protocol 15). Nodes below it aren't chosen to probe |
| Address check | The probe's dialer refuses loopback, link-local, multicast and metadata addresses, after DNS, with the same checks (`httpkit`) as alert channels and log drains. The host must be one of the service's domains |
| Down | Two failed probes in a row open an `uptime_down` alert (critical) keyed `uptime:<check id>`; the next success resolves it (the channel's `resolved` notification is the recovery) |
| History | `uptime_stats`: per check, minute rows for 24 hours and hour rows for 30 days (probes up and total, latency sum). Percentages for 24 h, 7 d and 30 d come from them; incidents are the check's alerts |
| Where shown | The service's uptime section in the UI (checks, 24 h strip, percentages, last incidents), `dock status` (24 h percentage and current state per service), `dock uptime list` |

## Scheduling

The scheduler is one goroutine with a min-heap of checks by next due
time and one `time.Timer` to the earliest. Adding, changing or removing
a check kicks it to rebuild its heap from the database. With no checks
the goroutine waits on its kick channel and the timer is stopped: an
install without checks has no new wake-ups. A due check is probed in its
own goroutine (bounded by a semaphore of 8), and its next due time is
its last due time plus the interval, so a slow probe doesn't drift the
schedule. A check whose probe is still running when it is due again is
skipped once.

The first probe of a check is spread over its first interval by its ID,
so many checks added at once don't fire together after a restart.

## Results

Each probe updates the check's in-memory state (consecutive failures,
last result) and adds to the current minute's row in one upsert. The
statistics tick (ADR 0015) rolls finished hours into hour rows and
trims minute rows older than 24 hours and hour rows older than 30 days,
as it does for statistics. A check at the floor interval writes two
small upserts a minute.

## Consequences

- "Production is down" reaches the alert channels within two intervals,
  judged from another server.
- One more always-armed timer while checks exist, documented in
  performance.md.
- A single-server install probes itself from the control plane: it sees
  the app and Caddy failing, not the network or DNS. The UI says which
  server probes.
