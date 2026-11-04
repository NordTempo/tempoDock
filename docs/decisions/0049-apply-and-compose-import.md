# 0049: Config as code with `dock apply FILE`, and importing Compose files

Status: accepted (decided by the agent, 2026-11-03).

## Context

[0022](0022-clone-and-export.md) added `dock project export` and left the
import for later: "merge or replace, what to do with services missing from
the file, how secrets get in, and how domains and git connections are
created". Two needs bring it back now:

- Config as code: a project file in git that recreates or updates a
  project, reviewed like code, applied by people, CI and agents.
- Moving apps in: most self-hosted apps ship a `docker-compose.yml`.

Forces:

| Force | Means here |
|---|---|
| Staging ([0007](0007-config-db-plus-repo-file.md), staged changes) | Every edit goes through staged changes: reviewed with `dock changes`, applied in one step, audited, discardable, and under the same permission checks (and environment locks) as edits from the CLI or UI. A file must not be a side door |
| Agent safety | An agent applying a file must never destroy something by accident: a service missing from a file is far more often a partial file than an intended deletion |
| Secrets stay secret | Values are sealed, readable only by admins; the export never holds one. A project file is committed to git, so it can't either |
| Round trip | `dock project export > dock.project.toml && dock apply dock.project.toml` must stage nothing |
| Small binary, small CLI | The client build (`-tags dockcli`) carries no YAML or Compose library; the daemon gains none it doesn't have |

## Decision summary

| Topic | Decision |
|---|---|
| Format | The export's format, extended (still `version = 1`; old exports stay valid). New optional keys: `values` and `shared_values` (variables whose value is in the file: literals and references), `depends_on` (first-deploy order hint). `variables` and `shared_variables` keep meaning "names whose values are set separately" (secrets) |
| Commands | `dock apply FILE [--plan\|--dry-run] [--prune] [--deploy] [-e ENV] [--project P]` stages a file; `dock apply` without FILE still applies staged changes. `dock import compose FILE [--project P] [-o OUT] [--apply]` |
| API | `POST /projects/{p}/plan` (read) and `POST /projects/{p}/apply` (deploy), body `ApplyRequest`; `POST /projects/{p}/import/compose` converts and plans, and with `apply` stages too |
| MCP | `plan_project_file` (read-only), `apply_project_file` (mutating), `import_compose` (mutating only with `apply`) |
| UI | New project › "From Compose": paste or upload, see the file, warnings and plan, create. The environment's staged changes list the new kinds |
| What applying does | Turns the plan into staged changes, nothing live. With `--deploy` it then applies each touched environment's staged changes, which is exactly `dock changes apply` |
| New staged kinds | Service creation, service archiving, domain addition and removal (migration 0051). Variables, settings and volumes were staged already |
| Deletions | Only with `--prune`, never by default. Without it the plan lists what the file doesn't mention as kept |
| What prune removes | Variables (staged unset), services (staged archive, admin only, applied by an admin only) and domains (staged removal). Never volumes or environments: they hold data and have no delete in Dock |
| Secrets | Never in the file. A name in `variables` must exist; a missing one is listed with the command that sets it. Values can come with the request (`values`), sealed and staged, never echoed |
| Environments | A file environment that doesn't exist is created empty (placed like `dock environment create`) and everything in it is staged. An empty environment runs nothing |
| Project | `apply` to a project that doesn't exist creates it (unlimited deploy, like `dock project create`) |
| Git connections, data settings | Compared, not applied: a difference is listed under `manual` with the command that changes it |
| Settings covered | Every staged service setting (image, port, commands, build, resources, health checks, pre-deploy command). A new setting is added in three places (`ServiceExport`, `serviceExport`, `desiredSettings`) and files cover it; registry credentials and other secrets the deploy features add are referenced by name, never inlined. Cron jobs get their own key when they exist |
| Environment lock ([0044](0044-environment-lock.md)) | Staging goes through `checkEnv` for each environment of the file before anything is written: a locked environment refuses a token's whole file as one refusal (so one access request), and the refusal quotes what the file would change there. Plans are reads and stay allowed |
| Compose parsing | The subset Dock can run, parsed with `gopkg.in/yaml.v2` (already in the daemon through Railpack: zero added binary). compose-go would add dozens of modules for features Dock can't use. Conversion runs in the daemon, so the CLI stays light and the UI uses the same code |
| Compose warnings | Every key that isn't mapped is reported (unsupported, ignored, or unknown). Nothing is dropped silently |

## The project file

```toml
version = 1
project = "shop"

[[environments]]
  name = "production"
  kind = "production"
  shared_variables = ["SENTRY_DSN"]          # values set separately
  [environments.shared_values]
    LOG_LEVEL = "info"

  [[environments.services]]
    name = "postgres"
    template = "postgres"

  [[environments.services]]
    name = "web"
    image = "ghcr.io/acme/web:1.4"
    port = 3000
    health_check_path = "/healthz"
    memory_mb = 512
    variables = ["STRIPE_KEY"]                # a secret: value set separately
    depends_on = ["postgres"]
    domains = [{ hostname = "shop.example.com" }, { hostname = "web-shop.dock.example.com", generated = true }]
    volumes = [{ name = "uploads", mount_path = "/app/uploads" }]
    [environments.services.values]
      DATABASE_URL = "${{postgres.DATABASE_URL}}"
      NODE_ENV = "production"
```

Rules, each enforced by the parser with a message naming the key:

- Unknown keys are errors (a typo never silently does nothing), as in
  `dock.toml`.
- A key may be in `values` or `variables`, not both.
- Names follow Dock's rules; duplicates are errors.
- A field left out means "not set" (zero), as the export writes it. For a
  template service it means the template's value, so `template =
  "postgres"` alone is a complete database.
- `depends_on` names services in the same environment. It is used only to
  order first deploys after an apply; it isn't stored and isn't exported,
  so it never shows as a change.
- Generated domains match any generated domain of the service: the
  hostname depends on the server's base domain, so a file moved to
  another install doesn't fight over it.

The export now writes reference-only values (`${{postgres.DATABASE_URL}}`,
nothing else around them) into `values`. They wire services together and
hold no secret, the same rule clone uses to copy them for any caller
(0022). Every other variable is still exported by name only, so a round
trip leaves them alone. This is the one change to 0022's "the value column
is never read on this path": the export now opens values to tell
references from secrets, and still writes only references.

## Plan

`plan` compares the file with the live state of each environment in it
(with `--environment`, just that one) and returns three lists:

| List | What |
|---|---|
| `changes` | What applying stages: environments and services to create, settings, variables (action only, never values), volumes, domains; with `--prune`, removals |
| `kept` | Live things the file doesn't mention: kept, and prune would remove the removable ones |
| `manual` | What needs a person: variables named in the file without a value, git connections and data settings that differ, a volume whose mount path differs, a generated domain on a server without a base domain |

The human output reads like a Terraform plan (`+`, `~`, `-`); `--json` is
the `ApplyPlan` value. The plan is computed against live values; staged
changes already in the environment are not part of the comparison. Applying
the plan replaces a staged change to an item the file sets, so after
`dock changes apply` the live state matches the file. Staged changes to
items the file doesn't mention are left alone.

Environment-limited tokens can plan and apply only their environment
(`--environment`): a file naming others is refused with that hint, since a
partial plan that silently skips environments would look complete.

## Staging the new kinds

| Kind | Stored as | Applied by `dock changes apply` | Discard |
|---|---|---|---|
| New service | A service row with `staged = 'create'`, its settings in the row, its variables and volumes staged against it | Clears the flag. A new service that can run from an image (an image setting or a template) gets its first release, in `depends_on` and reference order. A source service waits for `dock deploy` or a git push | Deletes the row and everything staged on it |
| Archive | `staged = 'archive'` | Archives the service like `dock service archive`. Needs admin to stage and to apply | Clears the flag |
| Domain | `staged_domains` (add with hostname or `generated`, or remove) | Runs after the transaction through the same code as `dock domain add` / `generate` / `remove`; a domain that fails stays staged and the result says why | Deletes the row |

A staged service can be named, given variables (`dock variable set -s web
...` on a staged service is how a secret gets its value before the first
deploy) and deployed: a deploy of it applies its own staged changes,
creation included, as deploys always did. Releases that don't apply staged
changes (promote, restart, rollback) refuse a staged service. Overview and
service lists show it with its staged state; it has no routes and no
container until applied.

Creating a service directly (`dock service create`, the UI, templates)
stays live, as before: only a file stages creation, because a file is
reviewed as a whole.

## Compose import

`dock import compose docker-compose.yml` sends the file (and the `.env`
and `env_file` files next to it) to the daemon, which converts it into a
project file and plans it. Nothing changes until `--apply`, which stages
the file with the detected secrets' values and applies, after a
confirmation. The UI does the same in New project.

| Compose | Dock |
|---|---|
| `image` | `image`; well-known databases become templates (below) |
| `build` (context, dockerfile) | A source service: `dockerfile_path` inside the context. Deployed with `dock deploy` from the context, or a git connection |
| `environment`, `env_file`, `.env` interpolation | `values`; secret-looking keys (password, secret, token, key, credential, private, salt) go to `variables` with their values kept out of the file and staged on apply; unresolved `${VAR}` without a default is a secret to set |
| `ports`, `expose` | `port` (the container port). A service publishing a port gets a generated domain when the server has a base domain. More than one port: the first is used, a warning names the rest |
| Named volumes | `volumes`. Bind mounts and tmpfs: warning, not mounted |
| `depends_on` | `depends_on` (order hint) |
| `healthcheck` | `health_check_path` when the test is an HTTP GET (curl, wget) of localhost; otherwise a warning. `start_period` sets the timeout |
| `command`, `entrypoint` | `start_command` (run with `sh -c` after the image's entrypoint) |
| `deploy.resources.limits`, `mem_limit`, `cpus` | `memory_mb`, `cpus` |
| `postgres`, `redis` images | The `postgres` / `redis` templates (backups, data panel). Other services' values pointing at them (a URL with the service's host, or the compose password) become references (`${{db.DATABASE_URL}}`, `${{db.POSTGRES_PASSWORD}}`); a warning says the template generates its own credentials |
| `privileged`, `network_mode: host`, `cap_add`, `devices`, `pid`, `ipc`, `user`, `secrets`, `configs`, replicas > 1, ... | Warning: unsupported, not applied |
| `restart`, `container_name`, `networks`, `labels`, `logging`, `x-*` | Ignored with a warning (Dock restarts services and gives every environment one network); `x-*` extension fields are silent, they are YAML anchors' material |

Compose names become Dock names (lower case, `_` and `.` to `-`); a name
that changes is a warning.

## Consequences

- A project can live in git and be recreated anywhere with one command,
  and an agent can propose a whole project as a file that a person reviews
  with `dock changes`.
- Staged changes now include service creations and archives and domain changes. The changes UI
  and CLI show them; discard can pick them.
- Removing things from a file does nothing until someone passes
  `--prune`, and removing a service needs an admin twice.
- Volumes and environments are never removed by a file.
- GitOps (a push to the default branch staging the file's plan) is a
  follow-up that reuses `POST /projects/{p}/apply` from the push handler.
- Performance: no new periodic work. A plan is a fixed number of queries
  per environment (the export's queries plus staged domains); apply is one
  write transaction per environment plus the domain operations.
