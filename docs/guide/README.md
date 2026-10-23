# Tempo Dock guide

How to install, use and automate Tempo Dock (`dock`). For the design and the
reasons behind it, see the [docs index](../../README.md#docs).

| Guide | For |
|---|---|
| [Install](install.md) | Putting `dock` on a server: requirements, `install.sh`, `config.toml`, upgrades, backups |
| [Getting started](getting-started.md) | From `dock login` to a live app with a domain |
| [Deploying](deploying.md) | Source vs image deploys, builds, health checks, cutover, rollback |
| [Variables](variables.md) | Shared vs service variables, references, staged changes, secrets |
| [Domains](domains.md) | Custom and generated domains, DNS, TLS, redirects |
| [Data](data.md) | Browsing, querying and changing database data; the data access policy |
| [Databases and backups](databases.md) | Postgres and Redis templates, daily backups, off-box repository, restore into a new service |
| [Agents](agents.md) | Letting Claude Code, CI and other bots deploy safely |
| [API](api.md) | Auth, OpenAPI spec, event stream, errors, curl examples |

## Words

| Term | Means |
|---|---|
| Project | A group of environments, e.g. `shop` |
| Environment | `production`, `staging`, ...: a set of services on one server with a private network. Its kind, production or non-production, decides which servers it may go to |
| Server | A machine running apps. It accepts production and/or non-production environments; a project keeps each kind on one server |
| Service | One container, built from source or a prebuilt image |
| Release | An immutable image + resolved config. Every deploy, rollback, restart and config change creates one |
| Variable | Shared (environment-level) or service-level, staged before it goes live |
| Domain | A public hostname routed to a service |
| Volume | A named Docker volume mounted into a service. Makes the service stateful |
| Token | An API credential with a scope (`read`, `deploy`, `admin`), optionally limited to a project or environment |

## Commands at a glance

Every command takes `--json`. Exit codes are in [CLI conventions](../cli.md#exit-codes).

| Area | Commands |
|---|---|
| Auth and servers | `dock login`, `dock logout`, `dock whoami`, `dock context add\|list\|use\|current\|remove`, `dock mcp` |
| Where you deploy | `dock link`, `dock unlink`, `dock project list\|create\|export\|archive\|pin`, `dock environment list\|create\|clone`, `dock service list\|create\|show\|update\|archive` |
| Deploys | `dock deploy`, `dock status`, `dock logs`, `dock releases`, `dock release list\|show\|rollback`, `dock rollback`, `dock restart`, `dock git connect\|status\|deploy\|disconnect`, `dock github status\|app\|install\|assign\|repos` |
| Config | `dock env set\|unset\|list\|import`, `dock changes`, `dock apply`, `dock discard`, `dock volume list\|add` |
| Domains | `dock domain add\|list\|remove\|generate` |
| Databases and backups | `dock service create NAME --template postgres\|redis`, `dock backup list\|run\|restore\|schedule\|status\|configure` |
| Data | `dock db status\|setup\|tables\|schema\|rows\|query\|history\|saved\|changes\|apply\|policy\|sensitive` |
| Admin | `dock token list\|create\|update\|revoke`, `dock user list\|create\|disable\|enable\|delete\|reset-password`, `dock audit`, `dock server list\|show\|update`, `dock address` |
| On the server | `dock serve`, `dock admin bootstrap`, `dock admin caddy-config`, `dock admin backup\|restore`, `dock node set-control-plane` |
