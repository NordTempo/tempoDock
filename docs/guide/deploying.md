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
| Promote | `dock promote staging production` | Nowhere: reuses the exact image that ran in staging | M3 |

A service's `image` setting (e.g. `postgres:17`) is the default for image
deploys when no reference is given, and changing it and applying deploys the
new image.
<!-- verify: how the CLI deploys a service's image setting without naming a reference (API: POST .../releases with an empty body). -->

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
  anything is built. Ignore large files or deploy an image.
  <!-- verify: too_large is HTTP 413, which cli.md maps to exit 1. -->

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

Build output is kept per release: `dock logs --build`.
<!-- verify: `dock logs` flags used across the guide: --build, --release <n>, --follow, --since (API: GET .../releases/{n}/build-log[/stream], GET .../logs?release&follow&tail&since) -->

## Settings: where they come from

For each setting, first match wins:

1. `dock.toml` in the uploaded source ([reference](../dock-toml.md)). Shown
   as locked in the UI and listed in the release's `config.from_repo_file`.
2. The service's settings (`dock service create`, `dock service set`).
   Changes are [staged](variables.md#staged-changes) like variables.
   <!-- verify: command for updating service settings (API: PATCH .../services/{service}, staged) -->
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

<!-- verify: `dock rollback` with no number picks the previous live release (the API always needs a number). -->

A rollback creates a **new** release with the old release's image and its
resolved config, through the same health-checked path. That includes the old
variable values: rolling back also rolls back config. Only releases that
went live (`active` or `superseded`) can be rolled back to. If the image is
no longer on the server it is pulled again, which only works for images
from a registry.

## Restart

```sh
dock restart
```

Creates a new release identical to the active one: same image, same resolved
config. It does not pick up staged changes; use `dock changes apply` for
that.

## Release sources

The `source` field of a release says what created it.

| Source | Created by |
|---|---|
| `upload` | Source deploy |
| `image` | Image deploy, or applying a change to the service's `image` setting |
| `config` | Applying staged changes, or adding a domain to a service whose port wasn't published yet |
| `rollback` | `dock rollback` (`from_release` is the copied release) |
| `restart` | `dock restart` |
| `promote` | `dock promote` (M3) |

## Archiving a service

`dock service archive web` stops the container and removes its routes.
Volumes, releases and history are kept. Needs the `admin` scope.
<!-- verify: CLI command name for archiving (API: POST .../services/{service}/archive). -->
