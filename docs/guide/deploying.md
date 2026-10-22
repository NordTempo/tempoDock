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
| Git push | GitHub webhook per service and branch | On a builder server | M4 |
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
  `[buildkit] memory_mb` (default 2048) and `cpus` (default 1), and a
  quarter of the normal CPU weight, so apps win any contention. A build that
  needs more memory fails with `build_failed` and says so; running apps are
  unaffected. Raise the cap or build in CI and deploy the image.
- **First Railpack build** downloads a ~150 MB version resolver to
  `/tmp/railpack`.
- Servers with `[buildkit] enabled = false` reject source deploys.

Build output is kept per release: `dock logs --build` (the newest build),
`dock logs --build --release 41`. Service output: `dock logs`, with
`--follow`, `--since 10m`, `--tail 500` and `--release N`.

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
| Start command | the image's own; a set command runs with `sh -c` |

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

## How a release goes live

```
queued → building → starting → health_checking → active
                         └──────────┴───────────→ failed
```

**Stateless services** (no volume) run side by side:

1. The new container starts next to the old one, on the environment's
   private network, with its own loopback port.
2. Health check.
3. Caddy's route switches to the new container in one atomic config load.
4. The old container keeps running for 12 s to drain connections still on it,
   then gets 30 s to stop gracefully.

**Stateful services** (any volume) are recreated, because two processes must
never share a volume: stop the old container, start the new one, health
check. Expect a short pause. If the new one fails, the old release is started
again.

**On failure** the new container is removed. For stateless services the old
one never stopped serving. The release is `failed` with `error` and
`error_code`: `build_failed`, `health_check_failed` or `deploy_failed`. The
CLI exits with code 6.

**If the daemon restarts mid-deploy**, the in-flight release is marked
`failed` ("daemon restarted during deploy") and the active one keeps
serving.

One deploy per service at a time: a second one gets `deploy_in_progress`
(exit code 5). Wait and retry.

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
variable values: rolling back also rolls back config. Only releases that
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
- Services are matched by name. One missing in production, with nothing
  running in staging, or still deploying in production is skipped and
  listed. Promote never creates services.
- It needs `deploy` on the target environment. The source's server is not
  involved, so promoting from the homelab works while it is off, as long
  as the image went through the [registry](install.md#registry).

## Release sources

The `source` field of a release says what created it.

| Source | Created by |
|---|---|
| `upload` | Source deploy |
| `image` | Image deploy (which also sets the service's `image` setting), or applying a change to that setting |
| `config` | Applying staged changes, or adding a domain to a service whose port wasn't published yet |
| `rollback` | `dock rollback` (`from_release` is the copied release) |
| `restart` | `dock restart` |
| `promote` | `dock promote`: the image of the source environment's active release, by digest |

## Archiving a service

`dock service archive web` removes the service's routes, then stops its
container in the background (up to 30 s; progress shows as events on the
last release). Volumes, releases and history are kept. Needs the `admin`
scope, and `--yes` when not on a terminal.

`dock project archive shop` does the same for every service in every
environment of the project, then hides it from `dock project list`
(`--all` shows it). An archived project takes no new environments or
services. It is refused while one of its services is deploying.

Archiving works while a service's server is unreachable; the answer (and
the audit log) says what was left:

- **Offline server:** its containers are stopped and its routes removed
  when it reconnects.
- **Revoked server:** containers on revoked server homelab were left
  running. Remove them on that machine (`docker ps --filter
  label=dock.managed=true`), or join it again with `dock server add` and
  they are removed when it connects.
