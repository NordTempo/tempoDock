# 0040: Traffic counts requests; visitors are Analytics'

Status: accepted (on Saile's request). Amends
[0033](0033-traffic-statistics.md).

## Context

A service with a domain has three tabs that sound alike: Metrics (CPU
and memory), Traffic ([0033](0033-traffic-statistics.md): what Caddy
served) and Analytics ([0037](0037-analytics.md): page views from the
site's script). Traffic and Analytics both showed visitors, counted two
ways: Traffic a fixed hash of address and user agent over every request,
Analytics a daily-salted hash over page views. The two numbers never
agree, which reads as a bug, and "Metrics" reads like analytics.

## Decision

| Topic | Decision |
|---|---|
| Traffic | Requests only: counts, status classes, bytes, time taken and paths. No visitors in the counter, the node link, the store, the API, `dock traffic` or the tab |
| Caddy's line | Without the client's address (`remote_ip`, `client_ip`) and without the `ua` field: the counter no longer reads them, so they aren't encoded or sent |
| Store | Migration 0035 drops `traffic_visitors` and `traffic_stats.visitors` |
| Node link | `HostTraffic` loses `visitors`. Older nodes still send it and it is ignored; no protocol change |
| The tab | The visitors chart gives way to the average time per minute or hour, from the points the tab already loads |
| Names | Metrics is Resources (`/resources`), like the Overview's row of the same numbers. Its shortcut stays G M |

## Consequences

- Visitors are on the Analytics tab, for sites that turn it on. A service
  without analytics has no visitor count.
- A counted request costs less on the node: no user agent scan, no hash
  and no sketch update (`BenchmarkCounterAdd`, 1.8 to 1.0 µs). The tick
  writes no sketches (`BenchmarkTrafficTick50`, 8.1 to 6.9 ms for 50
  busy services).
- No address or user agent of a visitor reaches the traffic counter.
- Links to `/metrics` stop working.
