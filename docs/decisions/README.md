# Decisions

Architecture decision records. Each one is short: context, decision,
consequences. When a decision changes, add a new record that supersedes the old
one instead of rewriting history.

Status is one of **proposed**, **accepted**, **superseded**.

| # | Decision | Status |
|---|---|---|
| [0001](0001-go-for-daemon-and-cli.md) | Go for the daemon and CLI | accepted |
| [0002](0002-single-binary-embedded-ui.md) | Single binary with embedded UI | accepted |
| [0003](0003-sqlite-for-platform-state.md) | SQLite for platform state | accepted |
| [0004](0004-caddy-as-separate-proxy.md) | Caddy as a separate proxy process | accepted |
| [0005](0005-openapi-api-with-generated-clients.md) | OpenAPI API with generated clients | accepted |
| [0006](0006-every-way-to-build.md) | Every way to build and deploy: source, git push, CI image, promotion | accepted |
| [0007](0007-config-db-plus-repo-file.md) | Config in the platform DB plus an optional repo file | accepted |
| [0008](0008-keep-doors-open.md) | Keep open source and hosting possible, cheaply | accepted |
| [0009](0009-dynamic-domains-and-tls.md) | Dynamic domains and automatic TLS | accepted |
| [0010](0010-environments-on-servers.md) | Environments are placed on servers (production vs homelab) | accepted |
| [0011](0011-stateful-services-and-databases.md) | Stateful services and databases | accepted |
| [0012](0012-name-tempo-dock.md) | Name: Tempo Dock, CLI `dock` | accepted |
| [0013](0013-homelab-ingress-cloudflare-tunnel.md) | Homelab ingress through Cloudflare Tunnel | accepted |

## Template

```md
# NNNN: Title

Status: proposed | accepted | superseded by NNNN

## Context
What forces are at play.

## Decision
What we do.

## Consequences
What gets easier, what gets harder.
```
