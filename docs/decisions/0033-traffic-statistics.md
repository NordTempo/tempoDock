# 0033: Traffic statistics for a service's domains

Status: accepted (decided by the agent, on Saile's request). Visitors
removed by [0040](0040-traffic-counts-requests.md).

## Context

Saile asked for statistics on every service with a domain: which
endpoints are requested, and how many people use it. Caddy already
fronts every domain, so it sees every request; the question is how to
count them without costing the apps or the idle daemon anything.

| Force | Means here |
|---|---|
| Hot path | Counting must not slow a request down, and a slow or stopped daemon must never hold Caddy up |
| Idle means idle | No polling and no new timers; a server without requests does nothing |
| Bounded | Memory and rows grow with services and time, never with requests or distinct URLs |
| Privacy | Counting people must not store who they are: no IP addresses, no cookies, nothing a visitor could be named from |
| Nodes | Each server counts its own traffic; the control plane only gets totals ([0014](0014-node-protocol.md)) |
| Stock Caddy | No plugins ([0004](0004-caddy-as-separate-proxy.md)) |

## Decision

| Topic | Decision |
|---|---|
| Source | Caddy's access log, for the hostnames of service routes only (`logger_names` with `skip_unmapped_hosts`). Redirect domains, Dock's own address and webhook routes aren't logged |
| Transport | Caddy's `net` log writer to `udp/127.0.0.1:7074` (`[caddy] traffic_listen`, empty turns it off), one datagram per line. The filter encoder drops request and response headers and TLS details; a `log_append` handler adds the user agent |
| Counting | On each server (`internal/node/traffic`), per hostname, in memory until drained: requests, status classes (1xx with 2xx), response bytes, summed duration, a visitor sketch and the most requested method and path pairs |
| Paths | The query is dropped and ID-like segments (numbers, UUIDs, long hex, long tokens with digits) become `:id`, at most 160 bytes. 256 paths per hostname between drains, kept with Space-Saving so a scanner can't push out real paths; a drain reports the top 50 |
| Visitors | A HyperLogLog sketch (1,024 registers, about 3% error) of a fixed hash of client address and user agent. Known bots and empty user agents count as requests, not visitors. Sketches merge, so a range's visitors are distinct across its hours |
| Collection | The stats tick ([0015](0015-server-pools-and-placement.md)) drains each server (`DrainTraffic`, node protocol 6) with its resource samples. A hostname counts only for a service on the server that reported it |
| Storage | `traffic_stats`: a point per service and minute (kept 24 hours) and per hour (30 days), added to on each tick. `traffic_visitors`: each hour's sketch, sparse-encoded. `traffic_paths`: requests per path and hour, trimmed to the top 100 once the hour is over. Pruning rides on the first tick of each hour |
| Reading | `GET .../services/{service}/traffic?range=24h\|7d\|30d`: points (minute or hour), totals, visitors and the top 20 paths, over whole hours since the hour the range starts in. `dock traffic [--range]`. A `traffic` event per environment and tick carries the new minute points |
| UI | A Traffic tab on the service page, for services with a domain (mock A, picked by Saile): totals, requests as bars with 4xx and 5xx stacked in, visitors over time, and the endpoint table with each path's share. The 24h view appends each minute from the `traffic` event |

## Why this shape

**The access log, not metrics.** Caddy's Prometheus metrics count by host
and status but have no paths and no visitors, and would mean scraping on
a timer. The access log has everything, and Caddy only writes it for the
hostnames it is told to.

**UDP, not a socket stream or a file.** A TCP or Unix stream blocks
Caddy's request goroutine when the reader falls behind, and a file has to
be tailed and rotated. A datagram is a single non-blocking send, and if
nothing reads it the line is lost instead of slowing anything. Loopback
UDP is reachable by local processes, like the internal listener Caddy
already calls for access checks; the worst a local process can do is add
fake counts.

**Count on the server, store totals.** Lines are parsed where they are
produced (about 2 µs per request, `BenchmarkCounterAdd`) and never cross
the node link or touch the disk. The control plane gets one small answer
per server per minute.

**Sketches, not visitor IDs.** A sketch answers "how many distinct" in
1 KiB whatever the traffic, merges across hours and servers, and keeps
only register maxima: no hash, address or user agent is stored or can be
recovered. The hash is fixed rather than salted per day, so a visitor
seen on Monday and Tuesday counts once for the week; salting matters for
schemes that store hashes, and this one doesn't.

## Consequences

- Counts start from the update that brings this; there is no backfill.
- Counted routes cost Caddy one JSON encoding and one loopback send per
  request. Routes that aren't counted are unchanged.
- While a server's daemon is stopped (an update, a crash), its Caddy can't
  send lines and writes them to its own stderr, the journal, until the
  daemon is back. Those requests aren't counted.
- Visitors are estimates: people behind one address with the same browser
  count once, one person on two networks counts twice. Single-page apps
  are counted by their requests, so in-app navigation that loads no page
  shows up as API calls, not page views.
- Path counts are per hour and keep each hour's top 100, so a long tail of
  rare paths is dropped, and the 24h range's paths start at a whole hour.
- Nodes older than protocol 6 report no traffic until they are updated.
- The tick's traffic step costs about 8 ms for 50 busy services
  (`BenchmarkTrafficTick50`), once a minute, and nothing for services
  without requests.
