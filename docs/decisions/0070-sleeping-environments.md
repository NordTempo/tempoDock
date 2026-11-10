# 0070: Sleeping environments (scale to zero)

Status: accepted (asked by Saile, 2026-11-10; plan and UI mock A picked
by Saile). Amends [0056](0056-preview-environments.md) (previews sleep
by default), [0055](0055-uptime-checks.md) (checks pause while asleep)
and [0014](0014-node-protocol.md) (protocol 22).

## Context

Previews ([0056](0056-preview-environments.md)) clone the base: a web
service, often a worker, and a template database. Most are looked at for
a few minutes after a push and then sit for days, each holding 150 to 300
MB on a server the apps share. Stopping a service by hand ([0050](0050-stop-start-service.md))
frees that, but nobody stops and starts previews by hand, and a stopped
service answers 503 until someone starts it.

| Force | Means here |
|---|---|
| Idle means idle ([performance](../performance.md)) | No new timer or poll to find idle environments |
| A visitor shouldn't notice much | Waking must take seconds, and the first request must get its answer, not an error |
| Stock Caddy ([0004](0004-caddy-as-separate-proxy.md)) | Holding requests with what Caddy has: no plugins |
| Nothing alerts on purpose | Health checks, uptime checks, crash loops and jobs must neither keep it awake nor call an asleep environment down |

## Decision

| Topic | Decision |
|---|---|
| What sleeps | The **environment** as a whole: every running service, template databases included. Services depend on each other; sleeping one while its database runs saves little and makes first requests a chain of cold starts. Services stopped on purpose stay stopped |
| How | `docker stop`, keeping the container (same release, port, volumes and writable layer). Waking is `docker start` (`Runtime.Resume`) and the release's own health check, services with volumes first, then the rest, each level side by side. Unlike 0050's stop, the container is kept: config only changes through a release, and every release wakes the environment first, so there is no stale config to fear, and waking needs no new container or log archive |
| Not a release | Sleeping changes whether a release runs, not what runs. A release per wake would add rows and deploy notifications per visit and run cutover each time |
| State | `environments.asleep_at` is the **target**: written before anything stops, cleared once every service passed its health check again. Recovery converges to it (`settleSleep`): a container of an asleep environment that runs after a crash or a host reboot is stopped again; the next request wakes it properly |
| Idle | The minute statistics tick already drains every server's traffic counts ([0033](0033-traffic-statistics.md)); a step after it notes which sleepable environments had requests (in memory) and sleeps those idle for `sleep_after_minutes`. No timer, no query unless one is due (`BenchmarkSleepPass50`: 1.2 µs, 0 allocations for 50 environments). A deploy in flight, a move, a command run or open requests keep it awake. A daemon restart starts every idle time over: it errs toward awake |
| Open requests | Caddy writes an access log line when a request ends, so a websocket or a stream reads as idle. Before sleeping, core asks the server's Caddy (`Runtime.OpenRequests`, the admin API's `/reverse_proxy/upstreams` `num_requests`): any open request keeps it awake for another timeout |
| Waking a request | An asleep environment's routes get a **wake check** (`Route.Wake`), built like the login access check ([0030](0030-access-protection.md)): after the route's access check, Caddy asks the internal listener's `/internal/caddy/wake`, which wakes the environment and holds the request. A 2xx lets it on to the app, by then running on the same port. A stranger at a login-protected preview never wakes it |
| Holding | Page loads (`Sec-Fetch-Mode: navigate`, or a GET for HTML) wait up to 3 s, then get a static waking page (503, `Refresh: 2`, no animation) whose reloads are held the same way. Everything else (fetch, webhooks, websocket upgrades) waits up to 30 s, then gets 503 with `Retry-After: 5`. At most 256 requests wait per environment; the rest get their answer at once. Once awake, the route is today's route again: an awake environment costs nothing extra per request |
| Uptime checks | Paused while asleep: no probe, no rows, no alert, and open `uptime_down` alerts resolve at sleep. On routes of sleepable environments, Dock's probes (`User-Agent: dock-uptime/*`) skip the access log (`Route.QuietProbes`), so a check every minute can't keep a preview awake; the wake check never wakes for one. Asleep is not downtime: status pages show it as neutral |
| Health checks | Docker stops probing with the container; container events of an asleep environment are ignored, and unhealthy and crash loop alerts resolve (status `asleep`) |
| Jobs ([0054](0054-scheduled-jobs.md)) | A scheduled run of an asleep environment is skipped, saying so. Run now, `dock run` and the Data panel wake it first; a run keeps it awake while it lasts |
| Deploys | Any deploy into an asleep environment wakes it first (deploying is a request to run it, as in 0050) |
| Settings | `environments.sleep_after_minutes`: 0 (never) or 5 to 1440. **Previews sleep after 30 minutes by default**, open ones included, from `preview_settings.sleep_after_minutes` (copied into each preview, and into open ones when changed; `--sleep-after off`). Other non-production environments opt in (`dock env sleep-after staging 2h`). Production is refused in core |
| Where | Every server. The wake check (`caddy.WakeHandler`) runs on each server's internal listener: on the control plane core wakes the environment (`RequestWaker`); on a node `caddy.NodeWaker` asks the control plane over the link and holds the request until the control plane applies that server's routes without the wake check. A node's wake request is an `ActionWake` event on the event stream the control plane already holds open (with the request, at most one per environment every 2 s, never queued while the link is down); the control plane wakes only an environment on that server. Before routes were applied after a node restart, every key reads asleep, so requests wait for the control plane rather than reach a stopped container |
| Node protocol 22 | Adds `resume` and `open-requests`, the routes' `wake` and `quiet_probes`, and wake events (`request`). A node below it gets no sleeping environment (`remote.SleepProtocol`): it would proxy to stopped containers. `Environment.sleep.blocked` says to update it. A server with traffic counting off can't tell idle either and says so; manual sleep still works |
| Permissions | Sleep now: deploy scope (it takes the environment from its users; a locked environment refuses tokens). Wake: **read** scope, since anyone who sees it can wake it by opening its address. The setting: admin of the project. Manual sleeps, wakes and setting changes are audited (`environment.sleep`, `environment.wake`, `environment.sleep_settings`); idle sleeps and request wakes aren't (their record is `slept_by`, `woke_by` and the `sleep` event) |
| API | `Environment.sleep` (state awake, sleeping, asleep or waking; since, last request, sleeps at, memory freed, who woke it as `GET /path/:id`, why it can't sleep), service state `asleep`, a `sleep` live event. `POST .../environments/{e}/sleep`, `POST .../wake`, `PUT .../sleep`; `PUT /projects/{p}/previews` takes `sleep_after_minutes` |
| CLI, MCP | `dock env sleep`, `dock env wake`, `dock env sleep-after ENV DURATION\|off`, `dock env previews enable --sleep-after`; `env list` and `status` show it. MCP `sleep_environment`, `wake_environment` |
| UI | Mock A: the environment's heading on the project and environment pages says `◌ asleep 2h`, the memory freed and "wakes on the next request", with Wake; services read `◌ asleep` (a dashed ring, info tone) like `○ stopped`; the service header has Wake; "Put to sleep now" is in the environment's menu; the Previews settings have "Sleep after" |
| Migration | 0081 |

## Consequences

- An idle preview gives back all its containers' memory, typically 150 to
  300 MB each, and comes back in a few seconds on its next request.
- In-memory state (sessions, caches) is lost on sleep. Environments that
  can't take that leave sleeping off.
- Bots that find a preview's hostname in certificate transparency logs
  wake a public one for a timeout; `woke_by` shows it, and access
  protection stops it.
- Apps that take longer than 30 s to start make programs retry; page
  loads keep reloading the waking page.
- A node's asleep environments wake only while its control plane is
  reachable: without the link, a request to one waits its 3 or 30 s and
  gets the waking page or a 503. Awake apps keep running either way.
