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
| Reading | Read in the environment, like logs. A service's list (`GET …/traces`, `dock traces`, MCP `traces_list`): entry spans, newest or slowest, by route, method, errors, release or since one, duration. One trace (`GET …/traces/{trace}?at=`, `dock traces show`, MCP `trace_get`): every span, across services, those of services outside the caller's token hidden |
| Sampling | All spans are kept up to the caps for now. The plan's ingest-side sampling (a per-service budget, errors and slow traces rescued from a ring of recently dropped spans) comes next |

## Consequences

- An app with an OpenTelemetry SDK is traced by turning tracing on and
  deploying; one without gets nothing new until Caddy's edge spans
  (phase 3).
- Tracing costs an idle server nothing; at 1,000 spans a second the
  ingest spends about 3 µs a span (`BenchmarkIngestProto512`).
- The analytics server runs a second container while tracing is on:
  about 25 MB idle, at most 512 MB.
- Spans don't survive a move of the analytics server or a restore of the
  control plane: they are diagnostics, kept for days.
- Still to come: sampling, Caddy's edge spans and per-route numbers
  (phase 3), the UI (phase 4), links from Traffic, Errors, releases and
  deploy diagnosis (phase 5), OTLP metrics (phase 6).
