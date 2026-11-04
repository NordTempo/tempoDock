# 0046: Log history past the container, and log drains

Status: accepted (decided by the agent)

## Context

An app's output lives only in Docker's `local` log driver, 10 MB x 3 per
container. When a release is replaced its container is removed, and its
logs go with it. A failed release keeps its last 200 lines on the control
plane (`saveContainerLogs`), a superseded one nothing: last night's crash
in the release before this one can't be read. There is no way to send
logs anywhere else either, so an operator with Loki or a syslog server
can't use it.

| Force | Means here |
|---|---|
| Idle means idle | No timers, no polling. Work happens when a container goes away, or while an app writes output |
| Stream, never buffer | Logs are copied Docker to gzip to file, and file to client, line by line |
| A small shared server | History is bounded per service in bytes and age; nothing grows without limit |
| Remote servers | Logs stay on the server that ran them and are read through the node link |
| A slow sink | Never blocks an app, the daemon or other sinks; lines are dropped and counted |

## Decision summary

| Topic | Decision |
|---|---|
| What is kept | When dock removes a release's container (replaced, failed, drift, archive), after it stopped, its whole Docker log is copied into the release's archive on the same server |
| Storage | `<data dir>/logs/<service id>/<release id>.jsonl.gz`: gzip'd JSON lines (`{"t","s","x"}`: time, stream, text). A release that had several containers (a stateful restart) appends a gzip member per container; gzip readers read them as one stream |
| Limits | `[logs] keep_mb_per_service` (64, compressed), `keep_mb_per_server` (2048, every service's together) and `keep_days` (30), from the control plane's config, passed with each call. Applied after each archive (oldest release files first) and on the hourly retention pass, which also removes the archives of services that no longer exist |
| A full disk | Nothing is archived while the data dir's filesystem has less than 1 GB or 5% free; the container is removed anyway. A disk that fills during an archive leaves the release's earlier archive as it was and no temporary file |
| Runtime interface | `ArchiveLogs(id, LogArchive)`, `ArchivedLogs(LogArchiveQuery)` and `PruneLogs(LogPrune)`, over the node link as `archive-logs`, `archived-logs` and `prune-logs` (protocol 14). An older node answers with an error: the release's last lines are saved on the control plane as before |
| Which server | `releases.logs_server_id` records where a release's archive is, so an environment moved to another server still finds its old releases' logs |
| Reading | `ServiceLogs` with a release whose container is gone reads its archive (tail and since as for Docker, follow ends after the backlog). `dock logs --release N`, MCP `logs` with `release`, and the logs tab's release picker use it |
| Drains | Per environment: `http` (JSON lines POST), `syslog` (RFC 5424, `tcp`, `tls` or `udp`) or `loki` (push API). Admin of the project configures them; targets are sealed like alert channels |
| Drain delivery | While an environment has a drain, the control plane follows each running service container's output (one `Logs` follow per container, started from container events) into a bounded queue per drain (10,000 lines or 4 MB of text, whichever is first). A sender per drain posts batches of up to 1,000 lines (1 MB), or what is queued after 1 s. A full queue drops new lines and counts them; a failing sink backs off (1 s doubling to 1 min) and keeps dropping while it does |
| SSRF | Drain targets go through the same address check as alert webhooks (no loopback, link-local, multicast or metadata), on every connection after DNS, syslog included; alerts and drains share it (`egressGuard`) |

## Log history

The archive is written by the server's runtime when core removes a
container: `removeContainer` stops it, calls `ArchiveLogs`, then removes
it. Stopping first means the archive holds everything the container
wrote. The copy streams Docker's log API (timestamps on) through a gzip
writer (`gzip.BestSpeed`) to a temporary file that is renamed into place,
or appended to an existing release file. A container's log is at most
30 MB (the driver's rotation), so an archive takes well under a second
and at most a few MB of disk; the file is the only thing the daemon
writes. Nothing runs while no container is removed.

Archiving is best effort: a failure is logged as a release event and
removal goes on, as a stuck archive must never keep an old release
running. Reading never needs the control plane's disk: the archive is
read where it was written, through the node link for a remote server,
and streamed line by line. A `tail` keeps only the last N lines in a
ring while it reads, so memory is bounded by the request.

The service's limits are applied right after each archive, so a service
that deploys often keeps the newest 64 MB of its history. Age is applied
then too, and on the hourly retention pass (`PruneLogs` per online
server, with the IDs of live services: a deleted service's directory
goes). `keep_mb_per_server` caps all services' history on a server
together (2 GB by default), the oldest archives going first whatever
their service, so many small services can't add up to a full disk.

Log history must never be what fills a disk, and on a nearly full one
the apps need what is left. So before writing, the runtime reads the
data dir's free space (one `statfs`): below 1 GB or 5% free it writes
nothing and says so (a release event, "the disk is nearly full"), and
core removes the container anyway. A disk that fills while a member is
written fails that write: the temporary file goes and the release's
earlier archive is left as it was. Serving never waits on any of it.

The 200-line copy on the control plane (`saveContainerLogs`) stays as a
fallback for nodes without protocol 14 and for releases from before
this; its files keep their retention (ADR 0020).

What this doesn't do: a long-running container's own log still rotates at
30 MB in Docker, and output older than that is gone. Keeping everything
an app ever wrote is what drains are for.

## Drains

A drain is configured on an environment (`dock logs drain add`, the
environment's settings). Kinds:

- `http`: `POST` of `application/x-ndjson`, one JSON object per line:
  `time`, `stream`, `text`, `project`, `environment`, `service`,
  `release`, `server`. Optional headers (for a token) are sealed.
- `syslog`: RFC 5424 messages, `APP-NAME` the service, `MSGID` the
  stream, severity 6 (stdout) or 3 (stderr). `tcp` and `tls` use octet
  counting (RFC 6587), `udp` one message per datagram.
- `loki`: `POST /loki/api/v1/push` with one stream per service and
  stream, labelled `project`, `environment`, `service`, `stream`. Basic
  auth or a bearer token optional, sealed.

The control plane delivers. It already follows a container's output for
`dock logs -f`, through the node link for remote servers, so drains need
no new node operation and no sink configuration on nodes. The cost is
that a remote server's drained output crosses the link once. A follower
starts when a service container starts (container event, or the drain is
added, or the daemon starts) and ends with the container's log stream;
it resumes after its last line's time when the container starts again.
Followers exist only for environments with a drain, so an install
without drains does nothing new.

Delivery never blocks a follower: each drain has a bounded queue, and a
full queue drops the line and counts it. The sender batches what is
queued (1,000 lines, or what arrived within a second of the first), and
on an error retries the batch with backoff while the queue keeps
filling and dropping. Per drain the UI and `dock logs drain list` show
lines sent, lines dropped, the last error and when it last delivered.
Counters are in memory: a restart starts them at zero.

Drained lines are the same lines `dock logs` shows, after the same
redaction of secret values.

## Consequences

- A superseded or failed release's logs can be read for 30 days (or
  until the service's 64 MB fill), on whichever server ran it.
- Removing a container takes a little longer: one stream of its log to
  disk.
- Each server's data dir gains up to `keep_mb_per_service` per service and `keep_mb_per_server` in all; none on a nearly full disk.
- With a drain, the control plane wakes for every batch of app output;
  without one, nothing changes.
- Drain counters reset on restart; lines that couldn't be delivered are
  dropped, never stored.
