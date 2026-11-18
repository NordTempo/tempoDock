# Deploying

Every deploy creates a **release**: an image plus the fully resolved config
(variables, port, limits, mounts, health check). A release only gets traffic
after its health check passes. Releases are immutable, so any release that
served traffic can be started again exactly as it was.

## Ways to deploy

| Path | Command | Image built | Status |
|---|---|---|---|
| Source | `dock deploy` | On the server, by BuildKit | Available |
| Image | `dock deploy --image ghcr.io/acme/web:1.4.2` | Elsewhere (CI); the server only pulls | Available |
| Git push | `dock git connect acme/shop --branch main`, then `git push` ([below](#deploy-on-git-push)) | On the server, by BuildKit, like `dock deploy` | Available |
| GitHub Actions | The Deploy to Tempo Dock action, after your tests ([guide](github-actions.md)) | On the server, or in CI for an image | Available |
| Promote | `dock promote staging production` | Nowhere: reuses the exact image that ran in staging ([below](#promote)) | Available |

### Image services and source services

A service's `image` setting says which kind it is:

- **Image service** (`image` set, e.g. `postgres:17`): every image deploy
  sets `image` to the image it deployed, so `dock deploy --image X` and the
  UI agree. The UI's **Deploy** button redeploys (and pulls again) the
  `image` setting, i.e. the last image deployed. Changing the setting with
  `dock service update --image` and applying deploys the new image.
- **Source service** (`image` empty): built from uploaded source. The UI's
  Deploy button is disabled; deploy with `dock deploy`.

A service switches kind with its deploys: deploying an image to a source
service makes it an image service, and a source deploy clears `image`
again. Rollbacks and restarts don't change the setting, so after rolling
back an image service, Deploy brings the newer image back.

The CLI always names the image (`dock deploy --image <ref>`); the API's
`POST .../releases` with an empty body deploys the `image` setting.

### Private registries

An image from a private registry is pulled with the organization's
credentials for its registry host ([ADR 0051](../decisions/0051-registry-credentials.md)).
An admin adds them once, in Settings › Registries or with the CLI:

```sh
printf %s "$GHCR_TOKEN" | dock registry login ghcr.io --username acme-bot --password-stdin
dock registry credentials          # hosts and usernames; passwords are never shown
dock registry logout ghcr.io
```

The host is as in the image reference: `ghcr.io`, `registry.example.com:5000`,
or `docker.io` for Docker Hub (also for images named without a host, like
`acme/web`). Images from other hosts are pulled anonymously. Use a
read-only token (for GitHub, a token with `read:packages`). Credentials
take effect on the next pull; a pull the registry refuses says which
command fixes it. Builds don't use them: a Dockerfile's `FROM` image must
be public.

Both paths take the service's own [staged changes](variables.md#staged-changes)
live with them.

## What a source deploy uploads

`dock deploy` packs the current directory (or linked directory) as a gzipped
tar:

- A root `.dockerignore` decides what's included, with Docker's rules.
- Without one, `.gitignore` files (root and nested) apply.
- `.git` is always left out. `Dockerfile`, `.dockerignore` and `dock.toml`
  are always kept.
- Uploads over `max_upload_mb` (default 512) fail with `too_large`, before
  anything is built (HTTP 413, exit code 1). Ignore large files or deploy
  an image.

## How the image is built

| Situation | Build |
|---|---|
| `build.dockerfile` in `dock.toml`, or the service's `dockerfile_path` | That Dockerfile |
| `Dockerfile` at the root | That Dockerfile |
| Neither | Railpack zero-config build. `build.command` / `build_command` overrides the build command |

- **Variables at build time.** Dockerfile builds get every variable as a
  build arg (declare `ARG NAME` to use it). Railpack builds get them as
  secrets, so they don't end up in image layers.
- **One build at a time per server.** A second build waits ("Waiting for
  another build on this server to finish").
- **Capped.** BuildKit runs in its own container (`dock-buildkit`) with
  `[buildkit] memory_mb` (default 2048) and `cpus` (default 0: half the
  server's cores, at least two), and a quarter of the normal CPU weight,
  so apps win any contention. A build that
  needs more memory fails with `build_failed` and says so; running apps are
  unaffected. Raise the cap or build in CI and deploy the image.
- **First Railpack build** downloads the version resolver Railpack plans
  with (mise, 55 MB, 150 MB unpacked) from its GitHub release to
  `/tmp/railpack`. Dock downloads it itself and uses it only when it
  matches the SHA-256 this Dock version was built with; a download that
  doesn't match fails the build and is not kept.
- Servers with `[buildkit] enabled = false` reject source deploys.

### Build caches

Each builder keeps one BuildKit cache, so a second build only redoes what
changed:

- **Layers** are shared by every build on the server. A step whose inputs
  didn't change (the same base image, files and command) is `CACHED`: with
  `COPY package.json pnpm-lock.yaml ./` before the install, a change that
  leaves the lockfile alone skips the install.
- **Cache mounts** (`RUN --mount=type=cache`) are kept per environment, so
  one environment can't read or change another's. Railpack builds use them
  for the package manager's store and framework caches (`.next/cache`) on
  their own. A preview environment starts with empty ones: a pull request's
  code is untrusted, and must not write into the caches its base
  environment's builds read.
- `[retention] build_cache_keep_mb` (default 2048) is what a disk prune
  leaves of the cache.

A Dockerfile build keeps only what its cache mounts name. Without one on
the step that runs `next build`, every Next.js build compiles the whole app
again, and the build log says so. Mount `.next/cache` (under the
`WORKDIR`) and the package manager's store:

```dockerfile
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm i --frozen-lockfile
COPY . .
RUN --mount=type=cache,target=/app/.next/cache pnpm run build
```

The same goes for other tools: `/root/.npm` for npm, `/usr/local/share/.cache/yarn`
for Yarn 1, `/root/.cache/pip` for pip, `/root/.cache/go-build` for Go. A
cache mount isn't part of the image, so don't put what the app reads at
runtime there.

Build output is kept per release: `dock logs --build` (the newest build),
`dock logs --build --release 41`. Service output: `dock logs`, with
`--follow`, `--since 10m`, `--tail 500` and `--release N`.

A release's output outlives its container. When a release is replaced,
fails or is archived, Dock copies what its container wrote to the server
it ran on before removing it, so `dock logs --release 41` still shows
last night's crash in the release before this one (`--follow` ends after
it). The logs tab has the same in its release list: the live release
first, then earlier ones with kept output. Each service keeps the newest
64 MB (compressed) for 30 days, and a server 2 GB in all; `[logs]` in
[config.toml](install.md#configtoml) changes that. On a disk with less
than 1 GB or 5% free nothing new is kept, so history never fills it: the
container is still removed, the release's events say its logs weren't
kept, and the deploy goes on. Kept output damaged by a crash or a full
disk reads up to the damage. `--grep` and secret masking apply to kept
output as to live output: grep matches the masked text. A server running a
dock older than the control plane keeps only a failed release's last 200
lines, on the control plane. Output a running container wrote before
Docker rotated it (30 MB per container) is gone.

### Log drains

A log drain sends the output of every service in an environment somewhere
else as the services write it ([ADR 0046](../decisions/0046-log-history-and-drains.md)).
Admins add them with `dock logs drain add` or under **Log drains** in the
project's **Settings**, **Variables**, under the environment's shared
variables:

```sh
dock logs drain add http https://logs.example.com/ingest --header X-Api-Key=...
dock logs drain add syslog tls://logs.example.com:6514
dock logs drain add loki https://loki.example.com --token - < token
dock logs drain list            # sent, dropped, last error, last sent
dock logs drain test http       # one test line, now
dock logs drain remove http
```

| Kind | What it sends |
|---|---|
| `http` | `POST` of `application/x-ndjson`, one JSON object per line: `time`, `stream`, `text`, `project`, `environment`, `service`, `release` |
| `syslog` | RFC 5424 over `tcp://`, `tls://` (octet counting) or `udp://` (a datagram per line); `APP-NAME` is the service, `MSGID` the stream, `HOSTNAME` `project.environment`, severity 6 for stdout and 3 for stderr. Ports default to 514, and 6514 for `tls` |
| `loki` | Loki's push API (`/loki/api/v1/push` is added to the URL), a stream per service and output stream, labelled `project`, `environment`, `service` and `stream` |

- **Secrets stay hidden.** URLs, headers and tokens are stored encrypted and
  never shown again; a user and password in an http(s) URL go as basic
  auth, `--token` as a bearer token. Lines are masked of the release's
  secret values, as `dock logs` shows them to someone who can't reveal
  variables.
- **Public addresses.** A drain can't send to the server's own addresses,
  link-local or cloud metadata addresses, or private ones (LAN, tailnet,
  container networks), checked again on every connection. A private
  address the operator lets apps reach (`[network] allow_private` in
  `config.toml`, see [App firewall](install.md#app-firewall)) is allowed
  too.
- **From now on.** A drain gets what services write after it is added, and
  after a restart of the daemon; a container that restarts resumes after
  the last line sent.
- **Never in the way.** Each drain queues at most 10,000 lines (4 MB) and
  sends them in batches of up to 1,000 lines, or what came within a
  second. When it can't keep up or its endpoint fails, new lines are
  dropped and counted, and it retries with backoff up to a minute. The
  counters start at zero when the daemon starts.

## Settings: where they come from

For each setting, first match wins:

1. `dock.toml` in the uploaded source ([reference](../dock-toml.md)). Shown
   as locked in the UI and listed in the release's `config.from_repo_file`.
2. The service's settings (`dock service create`, `dock service update`).
   Changes are [staged](variables.md#staged-changes) like variables.
3. Server defaults from `[defaults]` in `config.toml`.

| Setting | Default |
|---|---|
| Port | `8080`, also injected as `PORT` |
| Health check path | none (see below) |
| Health check timeout | 60 s |
| Memory | 512 MB |
| CPUs | 1 |
| Start command | the image's own; a set command runs with `sh -c` after the image's entrypoint, and one in exec form, a JSON array like `["worker", "--queue", "mail"]`, is passed to the entrypoint as arguments without a shell (for images whose entrypoint takes its mode as arguments) |
| Pre-deploy command | none; see [below](#pre-deploy-command) |
| Pre-deploy timeout | 10 minutes (at most 1 hour) |

Image deploys have no `dock.toml`; config releases and rollbacks keep the
`dock.toml` settings of the release they're based on.

Memory is a hard limit with no swap: an app that needs more is killed by
the kernel and restarted by Docker. A health check reports this as "ran out
of memory (limit N MB)", and so does `dock status` for a running service.
Raise `memory_mb` if that happens.

## Health checks

Checked every second until it passes or the timeout ends.

| Service has | Check |
|---|---|
| A health check path | HTTP `GET` on the path must return 2xx or 3xx |
| A domain, or a port set explicitly, but no path | TCP connect to the port |
| Neither (a worker, nothing published) | The container must stay running for 5 s |

Set a path for anything that serves HTTP. A TCP check passes as soon as the
port is open, before the app is ready.

### While the service runs

The same check keeps running after the release is live: Docker runs it
inside the container every 30 seconds, with `dock-probe`, a small probe
Dock mounts into the container, so it works in any image ([ADR
0043](../decisions/0043-ongoing-health-checks.md)). Workers without a port
have nothing to probe and get no ongoing check.

After 3 failed probes in a row the service is unhealthy: `dock status`,
the console and the MCP endpoint say so, an `unhealthy` alert goes to the
alert channels, and Dock restarts the container (same container, port and
volumes). If it keeps failing, restarts wait 1, 5, 15 and then 30 minutes.
When the check passes again the alert resolves, and the channels are told.

| Setting | Default | dock.toml `[deploy]` |
|---|---|---|
| `health_check_interval_seconds` | 30; -1 turns the ongoing check off | `health_check_interval = "30s"` or `"off"` |
| `health_check_probe_timeout_seconds` | 5 | `health_check_probe_timeout = "5s"` |
| `health_check_retries` | 3 | `health_check_retries = 3` |
| `health_check_start_period_seconds` | the deploy's health check timeout (60) | `health_check_start_period = "60s"` |

Failed probes during the start period after a start don't count, so a
slow app isn't restarted while it starts. A TCP check can't tell a hung
app from a working one (the kernel accepts connections for it): set a
health check path to catch hangs.

## How a release goes live

```
queued → building → starting → health_checking → active
   │                     └──────────┴───────────→ failed
   └──────────┴──────────┴──────────┴───────────→ canceled
```

**Pre-deploy command** first, if the service has one (in `starting`; see
[below](#pre-deploy-command)).

**Stateless services** (no volume) run side by side:

1. The new container starts next to the old one, on the environment's
   private network, with its own loopback port.
2. Health check.
3. Caddy's route switches to the new container in one atomic config load.
4. The old container keeps running for 12 s to drain connections still on it,
   then gets 30 s to stop gracefully.

A service's process runs under Docker's init, so the stop's SIGTERM reaches
it even when it would be PID 1 (a Node app, a start command run by a
shell), unless the image brings its own init (s6-overlay's `/init`, tini,
dumb-init, systemd). A container started before Dock did this gets it on
its next release.

**Stateful services** (any volume) are recreated, because two processes must
never share a volume: stop the old container, start the new one, health
check. Expect a short pause. If the new one fails, the old release is started
again.

**On failure** the new container is removed. For stateless services the old
one never stopped serving. The release is `failed` with `error` and
`error_code`: `build_failed`, `health_check_failed` or `deploy_failed`. The
CLI exits with code 6.

**Every step ends.** A deploy never waits forever: each step that waits
on something else has a deadline, and one that runs out fails the
release with the step in its error and diagnosis ("pulling ... didn't
finish in 30 minutes; deploy again").

| Step | Deadline |
|---|---|
| Downloading the source from GitHub (a push) | 10 minutes, before the release exists |
| Waiting for the platform registry (its images moving, a storage change) | 10 minutes |
| Waiting for another build on the same builder | as long as a build may run |
| The build and its push | `[buildkit] timeout_minutes` (30) |
| Pulling the image | 30 minutes |
| Starting the container | 5 minutes |
| The pre-deploy command, the health check | the service's own timeouts |

The registry's cleanup (a prune, which stops the registry while it
collects unused layers) never holds a build: a build that needs the
registry stops the collection, and the next prune finishes it. While a
release waits for the registry or a build slot, its progress says so.

**Why it failed.** Dock keeps a diagnosis of every failed release
([ADR 0071](../decisions/0071-deploy-diagnosis.md)), captured as it
fails: the likely cause from a small set of rules (the app listening on
another port than the one checked, an out-of-memory kill, a start command
the image lacks, a variable the app needs and the release doesn't set, a
health check path that answers 404, a failing pre-deploy command, ...),
what was ruled out, and the evidence: the health check's answer, the exit
code, what changed since the last release that served, and the last lines
of the container and the build. The release's page shows it with the fix
as its first button; `dock diagnose` prints it (`-r N` for an older one,
`--json` for all of it), and agents call MCP `diagnose`. When no rule
matches it says so and shows the evidence. A service keeps its last 20
for 30 days.

**What's in the image.** Once a release is live and no build or deploy
has run for a minute, Dock scans its image for known vulnerabilities on a
builder ([ADR 0074](../decisions/0074-image-vulnerability-scanning.md)):
Syft lists every package, Grype matches them against its database. A scan
never slows a deploy: one that arrives stops it, and it runs again later.
The service's overview says so only while the live image has critical or
high findings, with what fixes the worst: rebuilding on a newer base
image (operating system packages) or upgrading the app's dependencies.
Images in the sidebar lists every image the organization runs. `dock
vulns` prints a service's, `dock vulns --all --production` everything
in production, `dock scan` matches again, `dock sbom` prints the package
list (`--format cyclonedx-json` or `spdx-json` for other tools). An image built from a Dockerfile names the image it was
built on, and once a day Dock checks whether that tag has a newer image:
then a redeploy rebuilds on it and picks up its fixes. Production images are scanned only on builders that accept
production; CI images by the digest they were pulled by.

Each builder gets a newer vulnerability database once a day, and every
live image is matched again from its package list. A live release that
gets a critical vulnerability it didn't have alerts (`dock alerts vulns
high` for high ones too, `off` for none). When one doesn't matter where
it runs, ignore it from the image's page, or with `dock vulns ignore
CVE-2026-22451 --package sharp --reason "..."`: for the service, `--in
project` or `--in org` (admins), for 90 days (`--days`), or `--always`
(admins). It stops counting and alerting there and stays listed as
ignored; `dock vulns ignores` and Images, Ignores list every one.

The same happens when a live release crash loops, or stays unhealthy
after Dock restarted it: the alert waits a few seconds for the diagnosis
and says the likely cause, the service's page names it, and the
release's page shows the evidence (`dock diagnose` without `-r` shows the
newest).

**If the daemon restarts mid-deploy**, the in-flight release is marked
`failed` ("daemon restarted during deploy") and the active one keeps
serving.

One deploy per service at a time: a second one gets `deploy_in_progress`
(exit code 5). Wait and retry, or cancel the one that runs.

### Cancel a deploy

```sh
dock release cancel          # the release deploying now
dock release cancel 42       # that one, if it still deploys
```

Any step before traffic moves can be canceled: queued, building (pushing
and pulling too), the pre-deploy command, starting, the health check
([ADR 0086](../decisions/0086-cancel-a-deploy.md)). The service's page
has a Cancel action while a release deploys, and agents call MCP
`cancel_deploy`. It needs the deploy permission, like deploying.

What the release started is removed, as on a failure: a build stops, its
container goes, its port is freed. The release that served keeps serving
(a stateful service's, stopped first, starts again), and the changes the
release carried are staged again (`dock changes`). The release reads as
`canceled`, not `failed`: no alert, no diagnosis, nothing in Needs
attention. Following it (`dock deploy`, `dock release wait`) exits 10,
`deploy_canceled`.

Once the switch-over began it is too late: the cancel is refused, the
release goes live, and `dock rollback` takes you back. A pre-deploy
command that already finished (a migration) isn't undone; one that is
running is stopped where it is.

## Pre-deploy command

A command each new release runs before it gets traffic, typically database
migrations:

```sh
dock service update --pre-deploy-command "npx prisma migrate deploy" --pre-deploy-timeout 15m
dock apply
```

or in `dock.toml` (`deploy.pre_deploy_command`, `deploy.pre_deploy_timeout`,
`deploy.pre_deploy_snapshot`), or in the UI in the service's **Settings**.

Once the image is built or pulled, Dock runs the command with `sh -c` in a
one-off container of the **new** image, on the release's server: same
variables as the release (`DATABASE_URL` and the rest), same memory and CPU
limits, on the environment's private network, so `db:5432` resolves. It
mounts no volumes. Nothing has been started or stopped yet, stateful
services included, so the old release keeps serving while it runs. The
output goes to the release's build log (`dock logs --build`, the release
page in the UI).

With `--pre-deploy-snapshot` (off by default), every deployed stateful
service in the environment is [snapshotted](databases.md#snapshots) first,
the release's own included: a quick copy on the server to put back if the
migration goes wrong. Each snapshot is a line in the release's events
("Snapshot 12 of db taken before the pre-deploy command"). A failed
snapshot fails the release before the command runs, so nothing has
changed; fix the cause (usually disk) or turn the setting off with
`--pre-deploy-snapshot=false`.

- Exit 0: the deploy continues (start, health check, cutover).
- Any other exit code, or the timeout (default 10 minutes, at most 1
  hour): the release is `failed` with `deploy_failed`, the last lines of
  output are in its events, and the old release keeps serving. Nothing is
  rolled back in the database: write migrations that the release still
  serving can live with (expand first, contract in a later release).

Releases that build or pull a new image or change config run it: source
and image deploys, `dock apply`, git pushes and promotions. Rollbacks and
restarts don't, since they run an image that already served traffic (an
older migration tool may refuse a schema a newer one migrated); run the
command by hand if a rollback needs it. To skip it once, remove it, deploy,
and set it again.

## Rollback

```sh
dock releases                # newest first
dock rollback                # to the previous live release
dock rollback 41             # to a specific one
```

A rollback creates a **new** release with the old release's image and its
resolved config, through the same health-checked path. The CLI says what
it copies ("release 43 copies release 41, image ghcr.io/acme/web:1.4.1
(4be1c0ffee12), port 8080, 7 variables"), and the UI shows the same
before you confirm. That includes the old
variable values: rolling back also rolls back config, and the service's
own variables and settings (all but `image`) go back to that release's
values, so the next apply doesn't bring the newer ones back ([Variables](variables.md#who-can-see-what)
says what happens to shared ones). Only releases that
went live (`active` or `superseded`) can be rolled back to. If the image is
no longer on the server it is pulled again, which only works for images
from a registry.

## Restart

```sh
dock restart
```

Creates a new release identical to the active one: same image, same resolved
config. It does not pick up staged changes; use `dock apply` for
that.

## Run a command

```sh
dock run -s web -- bin/rails db:seed
dock run -it -s web -- bin/rails console
dock run -s app --volumes write -- ./fix-uploads.sh
dock runs -s web                 # the last 50 runs; `dock runs show <id>` for one's output
```

Runs a one-off command in a container of its own, next to the service:
the active release's image, its variables as they are live (not staged
changes), its CPU and memory limits and the environment's private network,
so it reaches the databases the service does. The command runs with
`/bin/sh -c`, so the image needs `sh`. It never touches the running
container, and works while the service is crash looping. `-i` passes
your stdin, `-t` gives it a terminal (with `-i`, a console); `dock run`
exits with the command's exit code.

| | |
|---|---|
| Who | Scope `deploy` on the environment. A locked environment refuses tokens like any change (ADR 0044) |
| Volumes | A database's volume is never mounted (connect over the network). Another stateful service's volumes are mounted read-only (`--volumes read`, the default), not at all with `--volumes none`, or writable with `--volumes write`, next to the running service: only for storage that takes a second writer. One writer per service at a time. A writer first takes a [snapshot](databases.md#snapshots) of the volumes (the run shows its ID); a failed snapshot refuses the run, and `--no-snapshot` skips it |
| Limits | `--timeout`, default 1 hour, at most 24 hours; 4 runs per service at once. `--detach` runs it in the background (`dock runs show <id>` for its output) |
| Record | Audited as `service.run` with the command and exit code. The service's last 50 runs keep their status and the last 16 KB of output, except terminal sessions, which keep no output |
| Secrets | The service's secret values are masked in the output, live and kept, for anyone who can't reveal them (admins can), as in logs. Code you run still reads them, so running needs deploy |

The service's Run tab in the UI runs commands too (without a terminal),
and agents have the MCP tool `run_command`. Design: [ADR
0048](../decisions/0048-one-off-commands.md).

## Stop and start

```sh
dock service stop web     # asks first; --yes in scripts
dock service start web
```

Stopping removes the service's container, freeing its memory, and its
domains answer `503` with a page saying it is stopped. Its active release,
port, volumes, domains and variables are kept, and `dock status` says who
stopped it and when. While it is stopped nothing restarts it: not daemon
recovery, drift checks or failing health checks, and it raises no alerts.
Scheduled backups skip it.

`dock service start` creates a release identical to the active one, like
`dock restart`, and follows it. The service runs again once that release
passes its health check; if it fails, the service stays stopped. Any
deploy to a stopped service also starts it when its release goes live. A server whose node is older than the control
plane answers `502` instead of the `503` page until it updates
([ADR 0050](../decisions/0050-stop-start-service.md)).

## Scheduled jobs

```sh
dock job set nightly -s web --schedule '0 3 * * *' --timezone Europe/Oslo -- bin/rails cleanup
dock job list -s web             # schedules, next times, last runs
dock job run nightly -s web      # now, outside the schedule
dock job history nightly -s web
dock job disable nightly -s web
```

A job is a command on a cron schedule, run like `dock run` (above) in the
service's active image, with its variables and network and without its
volumes. A service can have several. The schedule has the five usual
fields (`minute hour day-of-month month day-of-week`, with ranges, lists,
steps and names) or `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`,
read in the job's timezone (default UTC).

| | |
|---|---|
| Overlap | A job still running when it comes due again is skipped (`--overlap allow` runs both) |
| Downtime | A time missed while the control plane was down or the server offline runs once on recovery, however many were missed |
| Skipped | No active release, or the service is stopped (`dock service stop`): the run is recorded as skipped with why |
| Timeout | `--timeout`, default 1 hour, at most 24 hours |
| History | The last 20 runs per job, with exit code, duration and the last 16 KB of output |
| Failures | A failed or timed-out run raises a `job_failed` alert through the alert channels; the next good run resolves it |
| Who | Scope `deploy` to set, remove or run one; a locked environment refuses tokens, but its schedules keep running |

Jobs can live in `dock.toml` as `[[jobs]]` ([reference](../dock-toml.md));
they follow the active release. The service's Jobs tab edits them in the
UI, and agents have `list_jobs` (with `job`, its runs), `set_job`, `run_job` and `remove_job`.
Design: [ADR 0054](../decisions/0054-scheduled-jobs.md).

## Promote

```sh
dock promote staging production              # every service running in staging
dock promote staging production -s web,api   # only these
```

Deploys what is serving in one environment to another of the same project
([ADR 0014](../decisions/0014-node-protocol.md#4-registry-and-promotion)).
For each service, production gets a new release with the exact image of
staging's active release, pulled by digest from the registry, and
production's own variables and settings. Nothing is rebuilt, and a later
push to the same tag changes nothing.

- Services deploy one at a time through the normal health-checked path,
  and the CLI follows them like `dock deploy` (`--detach` returns once
  they are queued).
- The first failure stops it: the services after it fail without starting,
  the ones already promoted stay live, and the CLI exits 6.
- A service that already runs staging's image in production is left
  alone, and listed: promoting a project never restarts its unchanged
  database. `dock restart` is the command for that.
- Services are matched by name. One missing in production, with nothing
  running in staging, or still deploying in production is skipped and
  listed. Promote never creates services.
- It needs `deploy` on the target environment. The source's server is not
  involved, so promoting from the homelab works while it is off, as long
  as the image went through the [registry](install.md#registry).

## Clone an environment

```sh
dock environment clone staging staging-2               # nothing deployed
dock environment clone production staging --deploy     # and run the same images
```

Creates a new environment with the source's services, settings, variables
and volumes as they are live there ([ADR 0022](../decisions/0022-clone-and-export.md)).
It is placed like `dock environment create`: the kind comes from `--kind`
or the name, and it goes to the project's server for that kind.

| What | In the clone |
|---|---|
| Services and settings | Copied, archived services left out |
| Volumes | New and empty, created on each service's first deploy |
| Variables | Live at once. Values that are only references are always copied. Other values are copied if you may read them (admin), but never between production and non-production unless an admin passes `--copy-values` (the output then lists them). Otherwise database templates get new credentials and the rest are listed for you to set |
| Domains, git connections, releases, backups, staged changes | Not copied; the output counts them |

Nothing runs until you deploy. `--deploy` deploys the images serving in
the source like `dock promote` (by digest, one service at a time) and
follows them. It needs read on the source and deploy on the project.

## Move an environment to another server

```sh
dock env move staging --to homelab     # asks first, then follows each step
dock env move staging --status         # the running or latest move
```

Operators move an environment when a server joins or is retired
([ADR 0053](../decisions/0053-moving-environments.md), without downtime
since [ADR 0063](../decisions/0063-moves-without-downtime-and-server-drains.md));
the UI has the same in the environment's settings. Everything is checked
first: the server accepts the environment's kind, both servers are online
and on protocol 21, and the target has the memory, disk, images and
Cloudflare tunnels. Then:

| Step | What happens |
|---|---|
| prepare | Images, network and volumes are made on the target |
| sync | Volumes and snapshots copy while the services keep running, in passes that each send only what changed |
| stop | Caddy holds the environment's requests and its containers stop cleanly on the source |
| copy | A last exact pass, then a check pass that must find no difference; the services copy side by side |
| start | Every service starts on the target and passes its health check: the ones with volumes first, each group side by side |
| switch | The environment, routes, domains and placement point at the target, in one go; the hold ends |
| cleanup | The source's containers go |

Requests wait during the hold instead of failing. `--max-hold` (default
30 s, 5 to 600) bounds it: a move whose final copy would not fit fails
during sync, before anything stopped. A pass sends a changed file whole,
so the hold grows with the files written during the move, not with the
volume: a settled 400 MB Postgres taking writes to a small table held
requests for about 4 s on e2e servers, a freshly loaded one (autovacuum
rewriting its new 400 MB table) for 13 s. A failure before the switch rolls
back: the environment stays where it was, its data untouched. After the
switch the source keeps forwarding the services' domains to the target
until the source server is removed, so DNS can catch up; Cloudflare
domains follow by themselves, and the move lists the DNS records to
change for the rest. The source's volumes stay as a last copy until you
remove them (the move names them). A deploy during a move waits for it;
`dock run` and jobs run except in the hold; backups, snapshots and
restores are refused until it ends. The environment's lock applies to the
move itself. To empty a whole server, see
[draining a server](install.md#moving-environments-and-draining-a-server).

## Export a project's config

```sh
dock project export shop > shop.dock.toml
```

Prints the project's live configuration as TOML (`--format json` for
JSON): environments, services and their settings, volumes, domains, git
connections and variables. Secret values are never included: a variable
is listed by name, unless its value is only references
(`${{postgres.DATABASE_URL}}`). Everything is sorted by name, so the file
diffs well in git.

## Config as code: `dock apply FILE`

The export is also a project file you can apply
([ADR 0049](../decisions/0049-apply-and-compose-import.md), format in
[project files](../project-file.md)):

```sh
dock project export shop > dock.project.toml   # edit it, commit it
dock apply dock.project.toml --plan             # what would change (--json for agents)
dock apply dock.project.toml                    # stage it
dock changes -e production                      # review, like any staged change
dock apply -e production                        # make it live
```

Applying a file never changes anything live: the difference becomes staged
changes in each environment, reviewed and applied like edits from the CLI
or UI, under the same permissions, environment locks and audit log. New
services are staged too (they run nothing until applied); a new image or
database service gets its first release when the environment's changes
are applied. `--deploy` applies right after staging. A project or
environment the file names that doesn't exist is created, empty.

- **Nothing is removed by default.** Variables, domains and services the
  file doesn't mention are listed as kept. `--prune` stages their removal
  (archiving a service needs an admin, to stage and to apply). Volumes and
  environments are never removed by a file.
- **Secrets stay out of the file.** List a secret's name in `variables`;
  the plan says which ones still need a value and the command to set it.
  `values` holds the variables you do want in git: literals and
  references.
- `-e ENV` applies one environment of the file, which is what a token
  limited to one environment needs. A locked environment refuses a token's
  file as a whole, with what it would change there.
- Applying the same file twice stages nothing new.

## Move in from Docker Compose

```sh
dock import compose                          # plan ./docker-compose.yml as project <directory name>
dock import compose stack.yml -p shop -o dock.project.toml
dock import compose -p shop --apply          # create it
```

`dock import compose` converts a Compose file into a project file
([ADR 0049](../decisions/0049-apply-and-compose-import.md)) and shows its
plan; nothing changes without `--apply`. In the UI: New project › From
Compose.

| Compose | Dock |
|---|---|
| `image`, `build` | The service's image, or a source service (deploy it with `dock deploy <context>`) |
| `postgres`, `redis` images | Dock's database templates, with backups. Other services' URLs and passwords pointing at them become references (`${{db.DATABASE_URL}}`) |
| `environment`, `env_file`, `.env` | Variables. Passwords, tokens and keys are secrets: kept out of the file, their values staged from the Compose file (or asked for with `--apply` on a terminal) |
| `ports`, `expose` | The service's port; a published HTTP port gets a generated domain |
| Named and anonymous volumes | Named Dock volumes. Bind mounts and tmpfs aren't mounted (warned) |
| `depends_on`, `healthcheck`, `command`, `entrypoint`, resource limits | Start order, HTTP health check path, start command, memory and CPU limits |

Anything Dock can't run as written (privileged mode, host networking,
capabilities, devices, extra ports, replicas, ...) is printed as a
warning, never dropped silently.

## Deploy on git push

Connect a service to a GitHub repository and branch, and every push to it
deploys the service from source: the commit's files (or a subdirectory's)
go through exactly the pipeline of `dock deploy`, `dock.toml` included
([ADR 0018](../decisions/0018-git-push-deploy.md)).
To run your tests first and deploy only when they pass, deploy from
your workflow instead ([GitHub Actions](github-actions.md)).

```sh
dock git connect acme/shop --branch main
dock git connect acme/monorepo --branch main --dir apps/web
dock git status                                           # source, queued push, last delivery
dock git deploy                                           # the branch's head now (or --ref <sha>)
dock git disconnect
```

### Into a locked environment

A push into a [locked environment](agents.md#locked-environments-and-access-requests)
doesn't deploy by itself: it waits as an access request naming the
commit, and the commit status on GitHub says so. An admin approves it
in the UI's Inbox, and the push deploys as usual; a newer push
replaces the one waiting. This is production's deploy approval. Deploy in
the UI or `dock git deploy` by a person isn't held.

### Through Dock's GitHub App

Once an operator has set up Dock's GitHub App
([ADR 0031](../decisions/0031-github-app.md)), connecting needs nothing
on GitHub: no webhook, no token, private repositories included, and
commit statuses are on. One push deploys every service of the repository
whose branch and directory it touches.

```sh
dock address set dock.example.com     # GitHub delivers pushes here (operator, once)
dock github app create                # opens GitHub to create the App (operator, once)
dock github install default           # install it on an account, for an org (operator)
dock github status                    # the App, its webhook, installations and their orgs
dock github repos                     # what the org can connect
```

The console does the same under **System → GitHub**, and **Settings →
Source** suggests the org's repositories when connecting.

### A new service from a repository

With the App installed, a service can start from a repository: **New
service › GitHub** lists the repositories the App can read (the newest
push first), then the directories of one that look like an app (a
`Dockerfile`, `package.json`, `go.mod` and the like) at a branch. Tick
one or several: each becomes a service connected to its directory, and
the branch's head deploys at once. In a monorepo, pushes that change
nothing in a service's directory don't deploy it.

```sh
dock github paths acme/shop                       # directories that look like an app
dock service create web --repo acme/shop --dir apps/web
dock service create api --repo acme/shop --dir apps/api --branch staging
```

Connecting through the App checks that the branch exists and the
directory is one on it, so a typo fails at once.

The App is private by default: only the GitHub account that owns it
(yours, or `--owner acme`) can install it. With `dock github app create
--public`, any GitHub account can, so each org deploys from its own
([ADR 0066](../decisions/0066-github-apps-for-many-orgs.md)). Each
installation is assigned to one org, and only that org's services can
read its repositories. An install link is yours alone: it opens in the
browser you are signed in to Dock with, so sending it to someone else
installs nothing. After an install, GitHub sends the browser back to
Dock, then on to GitHub's authorize page for the App: if GitHub says you
can access the installation, Dock asks you to confirm the org and then
assigns it. Org admins install it for their own org with `dock github
install`. When someone else owns the GitHub account, they install the
App on GitHub and an operator assigns it. Installations made on GitHub directly are assigned by an
operator with `dock github assign <id> <org>`.

An App made before Dock kept its client secret (ADR 0066), so only
operators' install links work. `dock github status` shows the callback URL
to add in the App's settings on GitHub; generate a client secret there,
then `printf %s "$SECRET" | dock github app secret`.

Creating a new App (or installing it again) forgets the old
installations. Services connected through them keep their connection: the
first push or pull request the new installation delivers for their
repository moves them to it, if that installation is assigned to their
org. Until then they don't deploy on push; `dock git connect` again moves
one right away.

### Through a webhook

Without the App, or with `--mode webhook`, each service has its own
webhook:

```sh
dock git connect acme/shop --mode webhook
printf %s "$GITHUB_TOKEN" | dock git connect acme/shop --mode webhook --token-stdin --status
```

`connect` prints the webhook to add on GitHub (repository → Settings →
Webhooks): its Payload URL, content type `application/json`, the secret
(shown once; `--rotate-secret` makes a new one) and "Just the push event".
The UI does the same under the service's **Settings → Source**.

A token stays with the repository it was given for: changing the branch
or the directory keeps it, and connecting the service to another
repository needs a token for that one (`--token-stdin`), or an empty one
to go on without.

| What arrives | What happens |
|---|---|
| A push to the branch | Queued, then deployed as a source release with the commit SHA, subject and author on it (`deploy abc1234 fix checkout`) |
| A push while a deploy of the service runs | Waits for it; a newer push replaces it. A push never cancels a running deploy; you can ([Cancel a deploy](#cancel-a-deploy)) |
| Other branches, tags, deleted branches | Ignored |
| With `--dir`: a push changing nothing in the directory | Ignored |
| The commit already live | Ignored (`dock git deploy` deploys it anyway) |
| A bad or missing signature, an unknown webhook | Refused (401, 404) |
| A delivery ID seen in the last 7 days (a replay or a GitHub redelivery) | Answered, nothing deployed; use `dock git deploy` instead |

- **GitHub needs a public address to deliver to.** Dock's public address
  ([`dock address set`](install.md)) serves `/hooks/github/`. To keep the
  API private, set [`[hooks] public_host`](install.md#configtoml) instead
  to a hostname that points at the control plane (like an app domain):
  Caddy serves only `https://<public_host>/hooks/github/` there. Until
  there is one, `connect` works but says how to set it.
- **Private repositories over a webhook** need a fine-grained token with Contents read
  access to the repository; `--status` (commit statuses: pending, then
  success or failure, linking to the release when `[hooks] ui_url` is set)
  also needs Commit statuses read and write. Tokens are stored encrypted
  and never shown again: the API and UI only say whether one is set.
- Pushed releases are created by `github:<pusher>`; `dock git deploy`
  releases by whoever ran it. Connecting, changing and deploying need
  `deploy` on the environment.
- What happened to the latest delivery shows in `dock git status`, and in
  GitHub's delivery log (the response body).

## Release sources

The `source` field of a release says what created it.

| Source | Created by |
|---|---|
| `upload` | Source deploy, or a git push (`commit` set) |
| `image` | Image deploy (which also sets the service's `image` setting), or applying a change to that setting |
| `config` | Applying staged changes, or adding a domain to a service whose port wasn't published yet |
| `rollback` | `dock rollback` (`from_release` is the copied release) |
| `restart` | `dock restart`, or `dock service start` |
| `promote` | `dock promote`: the image of the source environment's active release, by digest |

Releases that reuse an image (`config`, `rollback`, `restart`, `promote`)
keep the `commit` of the release they copy, so the code a release runs is
always known.

## Archiving a service

`dock service archive web` removes the service's routes, then stops its
container in the background (up to 30 s; progress shows as events on the
last release). Volumes, releases and history are kept. Needs the `admin`
scope, and `--yes` when not on a terminal.

`dock project archive shop` does the same for every service in every
environment of the project, then hides it from `dock project list`
(`--all` shows it). Once their containers are stopped, its environments'
Docker networks are removed, so their addresses go back to `[network]
pool`. An archived project takes no new environments or services. It is
refused while one of its services is deploying.

Archived services can't be rolled back, so their images leave the
rollback window: the registry deletes them at its next prune (an archive
starts one unless one ran in the last hour; otherwise the next deploy
does), and a server whose disk fills up prunes their dangling copies.

Archiving works while a service's server is unreachable; the answer (and
the audit log) says what was left:

- **Offline server:** its containers are stopped and its routes removed
  when it reconnects. A project archive's networks there stay; the answer
  names them for `docker network rm` on that machine.
- **Revoked server:** containers on revoked server homelab were left
  running. Remove them on that machine (`docker ps --filter
  label=dock.managed=true`), or join it again with `dock server add` and
  they are removed when it connects.

## Deleting an archived project

An archived project keeps its volumes, history and name until it is
deleted. On its page, or in Organization settings, General, choose
**Delete permanently** and type its name, or run:

```sh
dock project delete shop --confirm shop
```

Its containers, networks, volumes and volume snapshots are removed from
its servers, its sites' analytics events from the analytics store, and
everything Dock knows of it (releases, variables, domains, jobs, previews,
the tokens limited to it) is deleted, which frees its name and its
domains. Its activity stays in the audit log. Its backups stay in their
restic repository: the answer names the repository and the tags that find
them, should you want them gone too. Only an archived project can be
deleted, by an admin of the whole organization, with every server it is on
online. It can't be undone ([ADR 0078](../decisions/0078-deleting-archived-projects.md)).

## Rename a project

On the project overview, choose **Rename project**, or run:

```sh
dock project rename shop storefront
```

This needs admin access to the whole project. Use the same lowercase name rules
as project creation. An archived project can be renamed too (its page, or
Organization settings), which frees its name for a new project.

The project's ID, services, containers, volumes, domains, tokens and release
history stay unchanged. Renaming does not redeploy anything, even if a deployment
is in progress. Open console pages follow the new address. Existing release
snapshots, including their `DOCK_PROJECT` value, stay as deployed; newly resolved
configurations use the new name.

Update bookmarked project URLs, explicit `--project` flags and API paths in
scripts. Old names do not redirect and may be reused. Current CLI versions
resolve linked directories by project ID; older clients need updating or
relinking. Historical audit targets retain the name used when the action happened.
