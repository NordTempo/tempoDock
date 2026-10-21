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
| [Agents](agents.md) | Letting Claude Code, CI and other bots deploy safely |
| [API](api.md) | Auth, OpenAPI spec, event stream, errors, curl examples |

## Words

| Term | Means |
|---|---|
| Project | A group of environments, e.g. `shop` |
| Environment | `production`, `staging`, ...: a set of services on one server with a private network |
| Service | One container, built from source or a prebuilt image |
| Release | An immutable image + resolved config. Every deploy, rollback, restart and config change creates one |
| Variable | Shared (environment-level) or service-level, staged before it goes live |
| Domain | A public hostname routed to a service |
| Volume | A named Docker volume mounted into a service. Makes the service stateful |
| Token | An API credential with a scope (`read`, `deploy`, `admin`), optionally limited to a project or environment |

## Commands at a glance

<!-- verify: this guide was written against the command set in docs/experience.md and the task brief while the CLI was being built. Check every command name and flag below against `dock <cmd> --help`. -->

Every command takes `--json`. Exit codes are in [CLI conventions](../cli.md#exit-codes).

| Area | Commands |
|---|---|
| Auth and servers | `dock login`, `dock logout`, `dock context add\|list\|use\|current\|remove` |
| Where you deploy | `dock link`, `dock unlink`, `dock project`, `dock environment`, `dock service` |
| Deploys | `dock deploy`, `dock status`, `dock logs`, `dock releases`, `dock rollback`, `dock restart` |
| Config | `dock env set\|unset\|list\|import`, `dock changes`, `dock changes apply\|discard`, `dock volume` |
| Domains | `dock domain add\|list\|remove\|generate` |
| Admin | `dock token`, `dock user`, `dock audit`, `dock server` |
| On the server | `dock serve`, `dock admin bootstrap`, `dock admin caddy-config` |
