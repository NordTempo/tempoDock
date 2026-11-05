# Project files

A project file describes a whole project: its environments, their
services, settings, variables, volumes and domains. `dock project export`
writes one; `dock apply FILE` reads one back, compares it with the live
state and stages the difference
([ADR 0049](decisions/0049-apply-and-compose-import.md)). It's TOML, or
the same structure as JSON.

Unlike [`dock.toml`](dock-toml.md), which lives in an app's repository and
holds how to build and run that one app, a project file holds everything
Dock stores about a project, except secrets.

```toml
version = 1
project = "shop"

[[environments]]
  name = "production"
  kind = "production"
  shared_variables = ["SENTRY_DSN"]        # set separately: dock env set --shared
  [environments.shared_values]
    LOG_LEVEL = "info"

  [[environments.services]]
    name = "postgres"
    template = "postgres"                  # Dock's database template, with backups

  [[environments.services]]
    name = "web"
    image = "ghcr.io/acme/web:1.4"
    port = 3000
    health_check_path = "/healthz"
    pre_deploy_command = "npm run migrate"
    memory_mb = 512
    variables = ["STRIPE_KEY"]              # a secret: its value is set separately
    depends_on = ["postgres"]
    volumes = [{ name = "uploads", mount_path = "/app/uploads" }]
    domains = [{ hostname = "shop.example.com" }, { generated = true }]
    [environments.services.values]
      DATABASE_URL = "${{postgres.DATABASE_URL}}"
      NODE_ENV = "production"
```

## Keys

| Key | Where | Meaning |
|---|---|---|
| `version` | top | Always `1` |
| `project` | top | The project's name; `dock apply -p` overrides it |
| `name`, `kind` | environment | `kind` is `production` or `non_production`; left out, `production` is production and anything else non-production. A kind never changes |
| `shared_variables` | environment | Names of shared variables whose values are set separately |
| `shared_values` | environment | Shared variables with their value in the file |
| `name` | service | Lower case letters, digits and dashes |
| `template` | service | `postgres` or `redis`. Its settings, volume and credentials come from the template; a setting in the file overrides the template's |
| `image`, `port`, `start_command`, `dockerfile_path`, `build_command`, `memory_mb`, `cpus`, `health_check_path`, `health_check_timeout_seconds`, `health_check_interval_seconds`, `health_check_probe_timeout_seconds`, `health_check_retries`, `health_check_start_period_seconds`, `pre_deploy_command`, `pre_deploy_timeout_seconds` | service | The service's settings, as `dock service update` names them. Left out means not set |
| `database`, `database_url_variable` | service | Data panel settings; compared, but changed with `dock db settings` |
| `variables` | service | Names of the service's variables whose values are set separately (secrets) |
| `values` | service | Variables with their value in the file: literals and references |
| `depends_on` | service | Services of the same environment that start first when an apply creates them. Not stored |
| `volumes` | service | `{ name, mount_path }` |
| `domains` | service | `{ hostname }`, `{ hostname, redirect_to }` or `{ generated = true }` (any generated domain of the service matches) |
| `git` | service | `{ repository, branch, directory, statuses }`; compared, connected with `dock git connect` |
| `stopped` | service | `true` for a service stopped on purpose. Compared, but stopping and starting act at once: the plan names `dock service stop` / `start` |
| `log_drains` | environment | `{ name, kind, target }`: where its logs go, without the URL's credentials. Compared; a missing drain is added with `dock logs drain add`, since its URL holds the secret |
| `registry_credentials` | top | Private registry hosts the project's images pull from with an organization credential. Only the host: the plan says which the organization lacks, and an admin adds them with `dock registry login` |

Parsing is strict: an unknown key, a key in both `variables` and
`values`, or `depends_on` naming a service that isn't there is an error
that says where.

## Secrets

A project file is meant for git, so it never holds a secret. Put the
secret's name in `variables` (or `shared_variables`): the plan lists any
that don't exist yet with the command that sets them, and `dock apply`
stages everything else. Set the value on the staged service before
applying (`dock env set -s web STRIPE_KEY=...`), or send values with the
file through the API (`values` in the request) or MCP. The export writes
only values made of references into `values`; every other variable is
exported by name.

## What applying does

| In the file, not live | Staged as |
|---|---|
| Environment | Created at once, empty (it runs nothing) |
| Service | A staged new service: it can be given variables and deployed, and runs nothing until the environment's changes are applied. Then a service with an image (or a template) gets its first release |
| Setting, variable, volume, domain | A staged change, as from the CLI or UI |

| Live, not in the file | Without `--prune` | With `--prune` |
|---|---|---|
| Variable, domain | Kept, listed | Staged removal |
| Service | Kept, listed | Staged archive (admin only, to stage and to apply) |
| Volume, environment | Kept, listed | Kept: a file never removes them |

Applying replaces a staged change to anything the file sets, so after the
environment's changes are applied the live state matches the file. Staged
changes to things the file doesn't mention stay staged.
