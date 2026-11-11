# Variables

Variables become environment variables in a service's container (and build
args at build time, see [deploying](deploying.md#how-the-image-is-built)).

## Scopes

| Scope | Set with | Received by |
|---|---|---|
| Shared (environment) | `dock env set KEY=value --shared` | Every service in the environment |
| Service | `dock env set KEY=value` (linked service) or `--service web` | That service only; overrides a shared variable with the same name |

`set`, `unset` and `import` take `--shared` or `--service/-s`; `dock env
list` shows the shared variables plus the service's (every service's
without one).

```sh
dock env set LOG_LEVEL=info --shared
dock env set DATABASE_URL=postgres://... --service api
dock env unset LEGACY_FLAG
dock env import .env                 # bulk set from a .env file
dock env list                        # names, scope and staged state; no values
```

Names: letters, digits and `_`, not starting with a digit.

## References

A value can reference another service's variable, or a shared one. They are
resolved when a release is created, so each release is self-contained.

| Syntax | Resolves to |
|---|---|
| `${{api.KEY}}` | Variable `KEY` as service `api` sees it (its own, shared, or injected) |
| `${{shared.KEY}}` | Shared variable `KEY` |
| `${{org.KEY}}` | Organization variable `KEY`, the same in every project |

```sh
dock env set API_URL='http://${{api.DOCK_PRIVATE_DOMAIN}}:${{api.PORT}}' --service web
dock env set DATABASE_URL='${{postgres.DATABASE_URL}}' --service api
```

Quote values with `${{ }}` in single quotes so the shell leaves them alone. A
reference to a missing service or variable, or a cycle, is an error, and
applying fails as a whole. `shared` and `org` can't be used as service names.

### Organization variables

Values every project needs, like an error tracker's DSN, live once in the
organization (Organization settings, Variables, or `dock org env`) and are
used where a variable says `${{org.KEY}}`. They are never added to
services by themselves. Admins set and reveal them; everyone sees which
services use each.

```sh
dock org env set SENTRY_DSN=https://...
dock env set 'SENTRY_DSN=${{org.SENTRY_DSN}}' --service web
dock org env redeploy SENTRY_DSN   # roll a new value out to every service using it
```

A changed value reaches a service on its next release; `redeploy` starts
one for each running service that uses it. An organization variable in
use can't be removed.

Services in an environment reach each other by service name on the
environment's private network, so `DOCK_PRIVATE_DOMAIN` is just the name.

## Injected variables

Set by the platform on every release. They win over your variables, and the
`PORT` and `DOCK_` names can't be set by hand.

| Variable | Value |
|---|---|
| `PORT` | The service's port setting (default `8080`). Listen on it |
| `DOCK_PROJECT` | Project name |
| `DOCK_ENVIRONMENT` | Environment name |
| `DOCK_SERVICE` | Service name |
| `DOCK_PRIVATE_DOMAIN` | Hostname on the private network (the service name) |
| `DOCK_PUBLIC_DOMAIN` | First non-redirect domain, if the service has one |

`DOCK_PUBLIC_DOMAIN` is resolved when the release is created; a domain added
later shows up on the next release.

### OpenTelemetry

While tracing is on (`dock tracing on`, operators), every service's
containers also start with the standard OpenTelemetry variables, so an app
instrumented with a stock OpenTelemetry SDK sends its traces to Dock with no
setup. Unlike the ones above, **your own value wins**: an app that already
sends to another backend keeps doing that, and `OTEL_SDK_DISABLED=true` turns
an app's SDK off.

| Variable | Value |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://dock-otlp:80`: the server's own Caddy, which passes exports to its trace ingest |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` (Dock takes OTLP over HTTP, protobuf or JSON, not gRPC) |
| `OTEL_EXPORTER_OTLP_HEADERS` | `authorization=Bearer%20dtr1...`: the release's own token, which can only send spans |
| `OTEL_SERVICE_NAME` | Service name |
| `OTEL_RESOURCE_ATTRIBUTES` | `service.namespace=<project>,deployment.environment.name=<environment>,service.version=r<release>` |
| `OTEL_METRICS_EXPORTER`, `OTEL_LOGS_EXPORTER` | `none`: Dock keeps logs from stdout, and no metrics yet |

They are set when a container starts, so turning tracing on or off reaches a
service with its next deploy or restart. Services on a server behind a
Cloudflare Tunnel don't get them yet.

## Staged changes

Setting and unsetting variables, changing service settings and adding volumes
are **staged**: stored next to the live values, not yet running.

```sh
dock env set FEATURE_X=on            # staged
dock changes                         # diff of everything staged in the environment
dock apply                           # make it all live
dock discard                         # or drop it all
```

| Change | Goes live when |
|---|---|
| Service variable, setting or volume | The next `dock deploy` of that service, or `dock apply` |
| Shared variable | Only `dock apply`, because it affects every service |

`dock env set ... --deploy` (also on `unset` and `import`) stages and
applies in one step.

Single changes can be dropped too; the rest stays staged:

```sh
dock discard FEATURE_X               # the linked service's FEATURE_X
dock discard LOG_LEVEL --shared      # a shared variable
dock discard -s web                  # everything staged for web
```

The UI's per-row undo does the same, for variables, settings and volumes
(API: `POST .../changes/discard` with a selection, see [API](api.md)).

Applying is one transaction, then one `config` release per affected running
service, each health-checked like any deploy. Affected means a service with
changes of its own, or one whose resolved variables change: every service
for a shared variable, and every service referencing a changed variable,
however indirectly (changing `postgres`'s `DATABASE_URL` also redeploys the
`api` that uses `${{postgres.DATABASE_URL}}`). `dock changes` lists them
under "Applying redeploys" (API: `affected`). Services that aren't running
pick the change up on their first deploy. A service already deploying is
listed under `skipped` and picks it up on its next release.

**If a release fails, its changes are staged again.** The values in effect
are those of the active release, so a failed release's variable, setting
and volume changes go back to staged (live values back to what is running),
and `dock changes` shows them with the failure (API: `last_failure`). Fix
them and apply again, or discard them. Nothing broken ships with the next
unrelated deploy. This covers a service's own changes and the shared
variables applied with it; a shared variable goes back to staged even if
other services already went live with it. A change made again since the
failed release is left as it is.

Setting a variable back to its live value un-stages it.

## Secrets at rest

- Variable values and every release's resolved config are encrypted with
  AES-256-GCM.
- The key is `/var/lib/dock/secret.key`, outside the DB, so a leaked
  `dock.db` alone reveals nothing. Back up both
  ([install](install.md#backups)).
- Values are never in API responses unless explicitly revealed. Release
  objects only list variable names (`config.variable_keys`).

## Who can see what

| Scope | Names | Set / unset / apply | Values |
|---|---|---|---|
| `read` | yes | no | no |
| `deploy` | yes | yes | no (write-only) |
| `admin` | yes | yes | yes, with `dock env list --reveal` or `dock changes --reveal` |

A `deploy` token can replace a value but never read one, including in the
staged-changes diff. Values still reach the app, so anything the app prints
to its logs is visible to whoever can read logs.

Rolling back restores the variable values of the release you roll back to.
