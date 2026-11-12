# Tracing

Dock keeps your apps' traces: where a request spent its time, across
services, down to the database call that failed. Apps instrumented with a
stock OpenTelemetry SDK send them with no setup: no collector, no vendor
account, no keys to manage.

## Turning it on

An operator turns tracing on for the whole Dock:

```sh
dock tracing on        # starts the trace store
dock tracing           # its state
```

The trace store runs in a container of its own on the analytics server
(`dock analytics server`) and keeps 7 days of spans (`[traces]
retention_days`). Turning tracing off (`dock tracing off`) removes it with
every span it holds.

## Sending traces

From each service's next deploy or restart, its containers start with the
standard OpenTelemetry variables (see [variables](variables.md#opentelemetry)):
where to send, the protocol, the service's own token, its name and its
project, environment and release. An app whose SDK reads them sends its
traces to Dock as they are:

- **Node.js**: `@opentelemetry/sdk-node` with `getNodeAutoInstrumentations()`.
- **Python**: `opentelemetry-distro` and `opentelemetry-exporter-otlp`, run
  with `opentelemetry-instrument`.
- **Java**: the OpenTelemetry Java agent (`-javaagent:opentelemetry-javaagent.jar`).
- **Go**: the `otlptracehttp` exporter, or `autoexport`. Not `otlptracegrpc`:
  Dock takes OTLP over HTTP only.
- **.NET**: `OpenTelemetry.Exporter.OpenTelemetryProtocol`, which follows
  `OTEL_EXPORTER_OTLP_PROTOCOL`.

Your own `OTEL_*` variables win. An app that sends its traces to another
backend (it sets `OTEL_EXPORTER_OTLP_ENDPOINT`, for example) gets none of
Dock's, so it keeps doing that and `dock traces` says so.
`OTEL_SDK_DISABLED=true` turns an app's SDK off.

Services on a server behind a Cloudflare Tunnel don't get the variables
yet.

Requests to a service's domains are traced from Dock's proxy too, so a
trace starts where the request reached the server, and an app with no
SDK at all still gets one span per request: its method, path, status
and time. The visitor's own trace headers are dropped.

## Reading traces

```sh
dock traces                                  # the service's traces, newest first
dock traces --slowest --since-release 42     # what got slow
dock traces --errors --route /orders         # what failed
dock traces show 4f2a9c1e…                   # one trace as a tree of its spans
```

In the UI, a service's Requests tab has a Traces view: a trace opens its
spans in place, and "Open full trace" gives it a page of its own. A failed
release's diagnosis links to the request that failed, and an error from a
service's logs links to its latest trace when the app sends the same
exception in a span.

Agents get the same through MCP: `traces`, which with `trace` reads one.

## App metrics

The same SDKs send their metrics to Dock too, when the service sets no
OTLP exporter of its own: the runtime's (heap, garbage collection, the
event loop, goroutines), the libraries' (a database pool's connections)
and your own counters and histograms. A service's Metrics tab shows
them below its CPU and memory, as charts:

- Until someone pins charts, Dock shows its own: the runtime metrics a
  stock SDK sends (`v8js.memory.heap.used`, `nodejs.eventloop.delay.p99`,
  `jvm.memory.used`, `go.memory.used`, `go.goroutine.count`, a pool's
  `db.client.connection.count` by state, and the like).
- **Add chart** pins any metric the service sends: how its points
  combine (a level's sum, max, min or average; a counter's rate a
  minute; a histogram's p50, p95, p99 or average) and an attribute that
  splits it into lines (at most 20, the biggest). **Edit** unpins.
  Pinning needs deploy access, like the service's other settings.

```sh
dock metrics                                               # the charts' latest values, and every metric sent
dock metrics show http.server.request.duration --agg p99 --by http.route
dock metrics pin queue.depth --agg max --by queue
```

Dock keeps a point a minute a series for the spans' retention (7 days),
and hourly points for 90 days, which charts over 7 and 30 days read. A
service keeps at most 2,000 series and 200 metrics a day (`[traces]
metric_series`, `metric_names`): past either, a new series' points are
dropped while the series it already sent go on, and the Metrics tab says
which metric and which of its attributes has the most values (often a
user ID or a URL): drop that attribute from the metric.

## What Dock keeps

- About 3,000 traces a minute a service: every trace of a quieter
  service, a fair share of a busier one, all spans of a trace together.
  Traces that fail (an error, an exception, a 5xx) or take a second or
  more are kept whatever the share, and `dock traces` says why each was
  kept. Dock's health checks and uptime checks are kept only when they
  fail.
- At most 2 million spans a service and 5 million an organization a
  day; past that only spans with an error. Past 5,000
  spans a second a service's spans are dropped, and its SDK is told so.
- Attribute values whose names look secret (`db.password`, `x-api-key`,
  `authorization` headers, `token` URL parameters) are masked before they
  are stored. Anyone who can't see your variables' values also gets those
  values masked, as in logs.
- Spans of services outside a token's project or environment are hidden
  from it.

## If a token leaks

Each release's token can only send spans for its own service. To revoke
every token at once, an operator rotates the trace key:

```sh
dock tracing rotate-key
```

Traced services get new tokens with their next deploy or restart; until
then Dock refuses their spans.
