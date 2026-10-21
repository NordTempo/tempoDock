# Variables

Variables become environment variables in a service's container (and build
args at build time, see [deploying](deploying.md#how-the-image-is-built)).

## Scopes

| Scope | Set with | Received by |
|---|---|---|
| Shared (environment) | `dock env set KEY=value --shared` | Every service in the environment |
| Service | `dock env set KEY=value` (linked service) or `--service web` | That service only; overrides a shared variable with the same name |

<!-- verify: flag names --shared and --service for env set/unset/list/import. API: PATCH .../variables, no `service` field = shared. -->

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

```sh
dock env set API_URL='http://${{api.DOCK_PRIVATE_DOMAIN}}:${{api.PORT}}' --service web
dock env set DATABASE_URL='${{postgres.DATABASE_URL}}' --service api
```

Quote values with `${{ }}` in single quotes so the shell leaves them alone. A
reference to a missing service or variable, or a cycle, is an error, and
applying fails as a whole. `shared` can't be used as a service name.

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

## Staged changes

Setting and unsetting variables, changing service settings and adding volumes
are **staged**: stored next to the live values, not yet running.

```sh
dock env set FEATURE_X=on            # staged
dock changes                         # diff of everything staged in the environment
dock changes apply                   # make it all live
dock changes discard                 # or drop it
```

| Change | Goes live when |
|---|---|
| Service variable, setting or volume | The next `dock deploy` of that service, or `dock changes apply` |
| Shared variable | Only `dock changes apply`, because it affects every service |

`dock env set ... --apply` stages and applies in one step.
<!-- verify: --apply flag on env set/unset/import (API: `apply: true`). -->

Applying is one transaction, then one `config` release per affected running
service, each health-checked like any deploy. Services that aren't running
pick the change up on their first deploy. A service already deploying is
listed under `skipped` and picks it up on its next release.

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
| `admin` | yes | yes | yes, with `dock env list --reveal` <!-- verify: --reveal flag name (API: `?reveal=true`) --> |

A `deploy` token can replace a value but never read one, including in the
staged-changes diff. Values still reach the app, so anything the app prints
to its logs is visible to whoever can read logs.

Rolling back restores the variable values of the release you roll back to.
