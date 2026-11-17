# 0055: Uptime checks from another server

Status: accepted (decided by the agent); amended by [0059](0059-analytics-area-and-slimmer-service-page.md) (checks that set themselves up), [0070](0070-sleeping-environments.md) (checks pause while the environment sleeps) and [0073](0073-public-status-pages.md) (day rows for 90 days)

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
| Who | Project admins add and remove checks; readers see results. An organization has at most 50 checks; non-operators see "another server" or "the control plane", not server names (ADR 0027) |
| Where it runs | On an online server other than the environment's, chosen by the check's ID among the online candidates so checks spread out; the control plane when there is no other server, and always for a domain behind a Dock login ([0030](0030-access-protection.md), "uptime probes"). The control plane's own probes run in process |
| Runtime interface | `Probe(ProbeRequest) ProbeResult` over the node link as `probe` (protocol 19, `remote.ProbeProtocol`). Nodes below it aren't chosen to probe. The prober is `internal/node/uptime` |
| Address check | Probes go to public sites only. The dialer, after DNS, refuses loopback, link-local, multicast, metadata, private (RFC 1918, ULA), CGNAT and tailnet addresses, which cover Docker's bridges, other apps' containers and LANs, and dials only ports 80 and 443. The server's own public address stays allowed: there it is Caddy, what a visitor reaches. The domain must be verified (its TXT record, ADR 0042), so its DNS is the owner's; removing it removes its checks. Errors are fixed phrases ("TLS: the certificate is invalid", "connection refused", from httpkit.NetErrorText), never raw dial or TLS errors, and a node's error is capped at 200 bytes without control characters |
| Down | Two failed probes in a row open an `uptime_down` alert (critical) keyed `uptime:<check id>`; the next success resolves it (the channel's `resolved` notification is the recovery) |
| History | `uptime_stats`: per check, minute rows for 24 hours and hour rows for 30 days (probes up and total, latency sum). Percentages for 24 h, 7 d and 30 d come from them, the strip is the last day in 48 half-hour buckets; incidents are the check's alerts (by key, `alerts_key`) |
| Where shown | The service's Uptime tab in the UI (checks, 24 h strip, percentages, last incidents), `dock status` (24 h percentage and current state per service), `dock uptime list` |

## Scheduling

The scheduler is one goroutine with a min-heap of checks by next due
time and one `time.Timer` to the earliest. Adding, changing or removing
a check kicks it to rebuild its heap from the database. With no checks
the goroutine waits on its kick channel, the timer is stopped and it
holds no bus subscription (it follows server events only while checks
exist to place): an
install without checks has no new wake-ups. A due check is probed in its
own goroutine (at most 8 at once and 2 per organization; a check
without a free slot is skipped that round, never queued, so one
organization probing a site that hangs can't hold up the others), and its next due time is
its last due time plus the interval, so a slow probe doesn't drift the
schedule. A check whose probe is still running when it is due again is
skipped once.

The first probe of a check is spread over its first interval by its ID,
so many checks added at once don't fire together after a restart.

## In the API

A check's expected status is a code such as `"200"`, or `"2xx"`/`"3xx"`, in the
API and CLI; the database and the node link store it as the code, or 2 or
3 for a class. Checks live under their service:
`/projects/{p}/environments/{e}/services/{s}/uptime-checks` (GET, POST)
and `.../uptime-checks/{id}` (DELETE). A check whose state changes (up or
down) is an `uptime` event, which open pages patch in. The project
overview carries a per-service summary (checks, state, 24 h uptime) from
one query, which `dock status` shows.

## Results

Each probe updates the check's in-memory state (consecutive failures,
last result) and adds to the current minute's row in one upsert. The
statistics tick (ADR 0015) rolls every hour that ended at least a minute
ago into hour rows and trims minute rows older than 24 hours and hour
rows older than 30 days, once an hour. A check at the floor interval
writes two small upserts a minute. `uptime_stats` is keyed by check
first, like `traffic_stats`, since every read is per check; rollups and
trims use an index on time.

A check's state, consecutive failures and last probe are in memory only:
after a restart a check is unknown (down while its alert is open) until
it is probed again. Alerts go through the alert engine (`signalAlert`,
key `uptime:<check id>`) like every other, so their channels, reminders
and resolutions are the same.

## Consequences

- "Production is down" reaches the alert channels within two intervals,
  judged from another server.
- One more always-armed timer while checks exist, documented in
  performance.md.
- A single-server install probes itself from the control plane: it sees
  the app and Caddy failing, not the network or DNS. The UI says which
  server probes.
