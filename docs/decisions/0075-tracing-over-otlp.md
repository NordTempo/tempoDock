# 0075: Tracing over OTLP

Status: accepted (the plan agreed with Saile, Postplan `8aawmml70o2a`,
mock B for the UI; Saile chose a trace store of its own after the shared
store failed its memory budget). Phases 1 to 2 built: #212, #234, #236,
#238 and this one.

## Context

When a Dock app is slow or answers 500, Dock says that it happens (the
Traffic tab, Errors, deploy diagnosis) but not where the time went or
which call failed. People pay Datadog, Honeycomb or Grafana Cloud for
that, or run Jaeger or Tempo on the small server Dock is trying to keep
free. Dock already knows the service, environment and release behind
every request and fronts it with Caddy, so it can be the trace backend
with no setup, and link traces to what it shows.

| Force | Means here |
|---|---|
| Zero setup | A stock OpenTelemetry SDK must send to Dock with only the variables Dock sets |
| Idle means idle | Nothing runs while no spans arrive; no scraping |
| Small servers | Bounded memory everywhere; a flood costs the app that floods, nothing else |
| Secrets | Span attributes can hold credentials; mask them as logs are |

## Decision

| Topic | Decision |
|---|---|
| Store | `dock-analytics traces`: the analytics binary, in a container of its own (`dock-traces`, its own volume, 512 MB, DuckDB at 128 MB) beside the analytics store, on the analytics server, with the analytics token. Spans in one table per UTC day: retention (`[traces] retention_days`, 7) drops whole days. Not in the control plane's backup; when the analytics server changes, the trace store starts empty there |
| Why its own | Attached to the analytics store, its CI peak reached 483 then 549 MB against the container's 512: bursts of writes grow DuckDB past its memory limit, and nothing we tried (trims after writes, lower limits, allocator, checkpoint and arena settings) moved that by more than 20 MB. Apart, analytics keeps its numbers |
| Turning it on | Operators: `dock tracing on/off`, `PUT /api/v1/tracing`, the console's Analytics page. Doesn't need analytics on. Off removes the trace store with its spans |
| Ingest | On every server (`[traces] ingest_listen`, 127.0.0.1:4318): OTLP/HTTP, protobuf (decoded by hand with `protowire`) and JSON; no gRPC (`OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf` covers stock SDKs). Metrics and logs are answered and dropped |
| Auth | A token per service and release, `dtr1.<org>.<project>.<env>.<service>.<release>.<mac>`, an HMAC under the install's trace key (derived from the analytics token, handed to every server with the salts while tracing is on). The ingest takes the service from the token, never from attributes. The forwarder keeps only spans of environments placed on the sending server, so a server's key can't speak for another's services |
| Reaching it | Every release's container gets `dock-otlp:host-gateway` in its hosts file and the standard variables, per signal so the token only goes to Dock: traces endpoint `http://dock-otlp:<port>/v1/traces` (the server's own Caddy, which routes that hostname from private addresses to the ingest), protocol, headers with the token, service name, resource attributes. The service's own variables win, and one that sends traces elsewhere (it sets the global or traces endpoint, the traces exporter, or turns the SDK off) gets none: its list says so. Metrics and logs are pointed at Dock, which drops them, only for a service that configures no OTLP itself (an SDK's default export to localhost logs an error a minute); never set to `none`. Set when a container starts. Not on tunnel-mode servers yet: Caddy listens on loopback there |
| Bounds | Bodies 2 MB (4 MB unpacked), two decodes at once (503 past), 5,000 spans a second per service, then OTLP partial success (SDKs don't resend). A span is cut on the way in: name, 64 attributes in 4 KB, values, 16 events in 12 KB, exceptions first. A spans spool per server (128 MB). Daily caps in the store: 2M spans a service, 5M an org, then error spans only (200k a service); 4 GB in all |
| Secrets | At ingest, sensitive attribute names (by word: `db.password`, `x-api-key`, not `total_tokens`), headers outside a short allowlist and sensitive URL parameters are masked. At read, a reader who can't reveal secret values gets each span's release's values masked, as in logs |
| Edge | A service's routes are traced from Caddy while tracing is on (`node.Route.Trace`, the service's trace token for its live release): after the access and wake checks, so refused and held requests make no trace, a `headers` handler drops the visitor's `traceparent`, `tracestate` and `baggage` (the internet can't pick trace IDs or force sampling), then stock Caddy's `tracing` handler starts a span, passes its traceparent to the app and writes the trace and span IDs into the request's access log line. Caddy exports nothing (`OTEL_TRACES_EXPORTER=none` in `dock-caddy.service`): the traffic counter, which reads those lines already (ADR 0033), hands traced ones to the trace ingest, which makes the edge span (kind edge, an entry span, the path as traffic counts it, the status and duration, no visitor address, user agent or query) and samples it like any span. Caddy's span ends last, so a slow or 5xx request rescues the app's spans. Not for redirects, stopped services, routes a server forwards for another, or a service that sends its traces elsewhere (its backend would get spans whose parent it never sees). A list shows a trace once, by its outer entry, so the edge span stands for the request. No "trust incoming trace context" setting yet |
| Per-route numbers | Counted at the edge from every request, never sampled, whether or not tracing is on: the traffic counter (ADR 0033) adds to each path's requests its 5xx answers and a histogram of how long they took (10 buckets, 5 ms to 2.5 s and past), drained on the minute tick and kept per hour with the paths (migration: `errors`, `lat0` to `lat9` on `traffic_paths`). `GET …/traffic`, `dock traffic` and the UI show each path's 5xx share and p50, p95 and p99, interpolated within buckets. Paths are Caddy's, IDs as `:id`, not the app's `http.route`: that would count only traced apps' sampled spans. The plan's separate route table in the trace store isn't needed |
| Reading | Read in the environment, like logs. A service's list (`GET …/traces`, `dock traces`, MCP `traces_list`): entry spans, newest or slowest, by route, method, errors, release or since one, duration. One row per trace: a trace that enters the service twice (it calls itself) is listed by its outer entry. One trace (`GET …/traces/{trace}?at=`, `dock traces show`, MCP `trace_get`): every span, across services, those of services outside the caller's token hidden |
| UI | Mock B: no new tab. The service's Requests tab (ADR 0076) gains a Traces view: the list with its filters in the address (range, newest or slowest, failed only, route, release), a row opening its waterfall in place (spans as a tree, bars at their offsets coloured by service, failures in red, at most 200 rows), a span its attributes and exception. "Open full trace" is the trace's page in the service shell, the root cause's span selected. Route-split and nothing live |
| Links | A failed release's diagnosis names the release's newest failed trace around the failure (ADR 0071's `trace` slot), read when the diagnosis is, since SDKs send spans seconds later; the CLI prints the command that shows it and the UI links to it. A span links to its release and to the service's log lines with its trace ID. The list takes `route=` and `release=`, so Traffic's endpoints and a release page can link to their traces |
| Sampling | At the ingest, per trace (`internal/traces/sample.go`). A trace's first span decides it from the trace ID's random bits against the service's threshold, so its spans get the same answer, and later spans of any service on the server follow. The threshold adapts each minute on the server itself (no tick from the control plane) so a service keeps about 3,000 traces a minute, never more in a minute. A span with an error, an exception or a 5xx, or an entry span of 1 s or more, rescues its trace: its dropped spans come back from a ring of recently dropped spans (4 MB per server, 1 MB chunks, the oldest recycled whole), and later spans follow; at most 600 ring scans a minute per service. Each kept span says the strongest reason its trace has so far, error, then slow, then sampled, so a trace's entry span (it ends last) says whether anything in it failed, and error spans are what the store's "errors only" cap keeps. The ring and the decisions (two generations of 32k trace IDs) exist only while spans arrive, freed by one timer two quiet minutes after the last. Dock's health probe sends `traceparent` with a `dockprbe` trace ID, and an app's span with the uptime or health probe's user agent marks its trace too: a probe's trace is never sampled and a slow one isn't rescued, but an error still rescues it, so a failing health check can be read like any request |

## Consequences

- An app with an OpenTelemetry SDK is traced by turning tracing on and
  deploying; one without still gets an edge span for each request to
  its domains: method, path, status and duration, with no
  instrumentation at all.
- Caddy's tracing handler adds nothing measurable per request: within
  ±20 µs of a 0.3 ms loopback round trip (2,000 requests, best of 5).
- Tracing costs an idle server nothing; at 1,000 spans a second the
  ingest spends about 3 µs a span (`BenchmarkIngestProto512`), and a
  rescue from a full ring about 4 µs (`BenchmarkSampleRescue`).
- A trace whose early spans left the ring before it was rescued (about
  10 s at 1,000 dropped spans a second) is stored partial.
- The analytics server runs a second container while tracing is on:
  about 25 MB idle, at most 512 MB.
- Spans don't survive a move of the analytics server or a restore of the
  control plane: they are diagnostics, kept for days.
- Still to come: OTLP metrics (phase 6). Errors don't take span
  exceptions as issues of their own: an app usually logs the same
  exception, so the Errors page would show it twice; linking a log issue
  to its latest trace is the follow-up.
